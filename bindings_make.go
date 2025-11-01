package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"maps"
	"slices"

	"github.com/refaktor/ryegen/v2/converter/typeset"
	"github.com/refaktor/ryegen/v2/typeutils"
)

func makeFuncBinding(f *types.Func, tset *typeset.TypeSet) binding {
	signature := f.Signature()

	var bf binding
	bf.typ = bindingFunc
	bf.pkg = f.Pkg()
	bf.requiredConverter = signature
	bf.funcCodeImports = []*types.Package{f.Pkg()}

	var fun string
	if signature.Recv() == nil {
		if pkg := f.Pkg(); pkg.Path() != "" {
			if pkg := tset.Qualifier()(pkg); pkg != "" {
				fun = pkg + "."
			}
		}
		fun += f.Name()
	} else {
		bf.funcCodeImports = append(bf.funcCodeImports, signature.Recv().Pkg())
		fun = fmt.Sprintf("(%v).%v", tset.TypeString(signature.Recv().Type()), f.Name())
	}
	bf.funcCode = fun

	bf.fillPropsAndRecv(f.Name(), tset)

	if f.Pkg().Path() == "golang.org/x/crypto/cryptobyte" && f.Name() == "ReadOptionalASN1Boolean" {
		// TODO: For some reason, the type checker gives us the wrong type here:
		// `func (*golang.org/x/crypto/cryptobyte.String).ReadOptionalASN1Boolean(*bool, bool) bool`
		// instead of `func (*golang.org/x/crypto/cryptobyte.String).ReadOptionalASN1Boolean(*bool, golang.org/x/crypto/cryptobyte/asn1.Tag, bool) bool`
		// I have absolutely no idea why this happens.
		bf.props.exclude = true
		return bf
	}

	return bf
}

func makeConstructorBinding(typ types.Type, tset *typeset.TypeSet) binding {
	// Figure out type object
	var obj types.Object
	{
		switch typ := typ.(type) {
		case *types.Alias:
			obj = typ.Obj()
		case *types.Named:
			obj = typ.Obj()
		default:
			panic("makeConstructorBinding: recvTyp must be Alias or Named, but got: " + typ.String())
		}
	}

	signature := types.NewSignatureType(
		nil, nil, nil,
		types.NewTuple(types.NewVar(token.NoPos, nil, "", typ)),
		types.NewTuple(types.NewVar(token.NoPos, nil, "", typ)),
		false,
	)
	bf := binding{
		typ: bindingConstructor,
		funcCode: fmt.Sprintf(`func(v %v) %v { return v }`,
			tset.TypeString(typ),
			tset.TypeString(typ),
		),
		pkg:               obj.Pkg(),
		requiredConverter: signature,
		funcCodeImports:   []*types.Package{obj.Pkg()},
	}
	bf.fillPropsAndRecv(obj.Name(), tset)
	return bf
}

func makeGetUnderlyingBinding(typ types.Type, tset *typeset.TypeSet) binding {
	// Figure out type object
	var obj types.Object
	{
		switch typ := typ.(type) {
		case *types.Alias:
			obj = typ.Obj()
		case *types.Named:
			obj = typ.Obj()
		default:
			panic("makeGetUnderlyingBinding: recvTyp must be Alias or Named, but got: " + typ.String())
		}
	}

	under := typ.Underlying()
	signature := types.NewSignatureType(
		types.NewVar(token.NoPos, nil, "", typ),
		nil, nil, nil,
		types.NewTuple(types.NewVar(token.NoPos, nil, "", under)),
		false,
	)
	bf := binding{
		typ: bindingConstructor,
		funcCode: fmt.Sprintf(`func(v %v) %v { return (%v)(v) }`,
			tset.TypeString(typ),
			tset.TypeString(under),
			tset.TypeString(under),
		),
		pkg:               obj.Pkg(),
		requiredConverter: signature,
		funcCodeImports:   []*types.Package{obj.Pkg()},
	}
	bf.fillPropsAndRecv("(?)", tset)
	return bf
}

func makeFieldGetterBindings(typ types.Type, tset *typeset.TypeSet) []binding {
	var pkg *types.Package
	switch t := typ.(type) {
	case *types.Alias:
		pkg = t.Obj().Pkg()
	case *types.Named:
		pkg = t.Obj().Pkg()
	default:
		return nil
	}
	struc, ok := typ.Underlying().(*types.Struct)
	if !ok {
		return nil
	}

	var bindings []binding
	for field := range struc.Fields() {
		if !field.Exported() {
			continue
		}
		maybeAddrStr := ""
		returnType := field.Type()
		if _, ok := returnType.Underlying().(*types.Struct); ok {
			// If the getter returns a struct, we want to return a pointer,
			// so that the returned value is addressable. This way, it's possible
			// to manipulate nested structs.
			returnType = types.NewPointer(returnType)
			maybeAddrStr = "&"
		}
		signature := types.NewSignatureType(
			types.NewVar(token.NoPos, nil, "", types.NewPointer(typ)),
			nil, nil, nil,
			types.NewTuple(types.NewVar(token.NoPos, nil, "", returnType)),
			false,
		)

		var requiredImports []*types.Package
		if pkg != nil {
			requiredImports = append(requiredImports, pkg)
		}

		requiredImports = append(requiredImports, typeutils.CollectImports(field.Type())...)
		bf := binding{
			typ: bindingGetter,
			funcCode: fmt.Sprintf(`func(s *%v) %v { return %vs.%v }`,
				tset.TypeString(typ),
				tset.TypeString(returnType),
				maybeAddrStr,
				field.Name(),
			),
			pkg:               pkg,
			requiredConverter: signature,
			funcCodeImports:   requiredImports,
		}
		bf.fillPropsAndRecv(field.Name()+"?", tset)
		bindings = append(bindings, bf)
	}
	return bindings
}

func makeFieldSetterBindings(typ types.Type, tset *typeset.TypeSet) []binding {
	var pkg *types.Package
	var objName string
	switch t := typ.(type) {
	case *types.Alias:
		pkg = t.Obj().Pkg()
		objName = t.Obj().Name()
	case *types.Named:
		pkg = t.Obj().Pkg()
		objName = t.Obj().Name()
	default:
		return nil
	}
	struc, ok := typ.Underlying().(*types.Struct)
	if !ok {
		return nil
	}

	var bindings []binding
	for field := range struc.Fields() {
		if !field.Exported() {
			continue
		}
		if pkg != nil && pkg.Path() == "testing" && objName == "B" && field.Name() == "N" {
			// Should not set (testing.B).N
			continue
		}
		signature := types.NewSignatureType(
			types.NewVar(token.NoPos, nil, "", types.NewPointer(typ)),
			nil, nil,
			types.NewTuple(types.NewVar(token.NoPos, nil, "", field.Type())),
			types.NewTuple(types.NewVar(token.NoPos, nil, "", types.NewPointer(typ))),
			false,
		)

		var requiredImports []*types.Package
		if pkg != nil {
			requiredImports = append(requiredImports, pkg)
		}
		requiredImports = append(requiredImports, typeutils.CollectImports(field.Type())...)

		bf := binding{
			typ: bindingSetter,
			funcCode: fmt.Sprintf(`func(s *%v, v %v) *%v { s.%v = v; return s }`,
				tset.TypeString(typ),
				tset.TypeString(field.Type()),
				tset.TypeString(typ),
				field.Name(),
			),
			pkg:               pkg,
			requiredConverter: signature,
			funcCodeImports:   requiredImports,
		}
		bf.fillPropsAndRecv(field.Name()+"!", tset)
		bindings = append(bindings, bf)
	}
	return bindings
}

func makeGlobalGetterBinding(obj types.Object, tset *typeset.TypeSet) binding {
	maybeAddrStr := ""
	returnType := resolveUntyped(obj) // consts may be untyped
	if _, ok := returnType.Underlying().(*types.Struct); ok {
		// If the getter returns a struct, we want to return a pointer,
		// so that the returned value is addressable. This way, it's possible
		// to manipulate nested structs.
		returnType = types.NewPointer(returnType)
		maybeAddrStr = "&"
	}
	signature := types.NewSignatureType(
		nil, nil, nil, nil,
		types.NewTuple(types.NewVar(token.NoPos, nil, "", returnType)),
		false,
	)
	bf := binding{
		typ: bindingGetter,
		funcCode: fmt.Sprintf(`func() %v { return %v%v }`,
			tset.TypeString(returnType),
			maybeAddrStr,
			objectString(obj, tset.Qualifier()),
		),
		pkg:               obj.Pkg(),
		requiredConverter: signature,
		funcCodeImports:   []*types.Package{obj.Pkg()},
	}
	bf.fillPropsAndRecv(obj.Name()+"?", tset)
	return bf
}

func makeGlobalSetterBinding(obj types.Object, tset *typeset.TypeSet) binding {
	signature := types.NewSignatureType(
		nil, nil, nil,
		types.NewTuple(types.NewVar(token.NoPos, nil, "", obj.Type())),
		nil,
		false,
	)
	bf := binding{
		typ: bindingSetter,
		funcCode: fmt.Sprintf(`func(x %v) { %v = x }`,
			tset.TypeString(obj.Type()),
			objectString(obj, tset.Qualifier()),
		),
		pkg:               obj.Pkg(),
		requiredConverter: signature,
		funcCodeImports:   []*types.Package{obj.Pkg()},
	}
	bf.fillPropsAndRecv(obj.Name()+"!", tset)
	return bf
}

func makeMethodBindings(recvTyp types.Type, tset *typeset.TypeSet) []binding {
	var bindings []binding
	if iface, ok := recvTyp.Underlying().(*types.Interface); ok {
		if !iface.IsMethodSet() {
			// Contains type constraints
			return nil
		}
	} else {
		// Only non-interface can be pointer receiver
		recvTyp = types.NewPointer(recvTyp)
	}

	// Figure out receiver type object
	var recvObj types.Object
	{
		rt := recvTyp
		if p, ok := rt.(*types.Pointer); ok {
			rt = p.Elem()
		}
		switch rt := rt.(type) {
		case *types.Alias:
			recvObj = rt.Obj()
		case *types.Named:
			recvObj = rt.Obj()
		default:
			panic("makeMethodBindings: recvTyp must be (Pointer to) Alias or Named, but got: " + recvTyp.String())
		}
	}

	ms := types.NewMethodSet(recvTyp)
	for m := range ms.Methods() {
		m := m.Obj().(*types.Func)
		if !m.Exported() {
			continue
		}
		{
			// Receiver is currently the receiver the method was declared on.
			// Set receiver to the receiver type determined by the consumer.
			sig := m.Signature()
			m = types.NewFunc(m.Pos(), recvObj.Pkg(), m.Name(),
				types.NewSignatureType(
					types.NewVar(recvObj.Pos(), recvObj.Pkg(), "", recvTyp),
					nil, //slices.Collect(sig.RecvTypeParams().TypeParams()),
					nil, //slices.Collect(sig.TypeParams().TypeParams()),
					sig.Params(),
					sig.Results(),
					sig.Variadic(),
				),
			)
		}

		bindings = append(bindings, makeFuncBinding(m, tset))
	}
	return bindings
}

func makePkgBindings(tset *typeset.TypeSet, typesInfo *types.Info, files []*ast.File) []binding {
	var bindings []binding
	namedOrAliasTypes := map[string]types.Type{} // must be Named or Alias to Named
	structAliasTypes := map[string]*types.Alias{}
	for _, f := range files {
		for _, decl := range f.Decls {
			switch decl := decl.(type) {
			case *ast.FuncDecl:
				if !decl.Name.IsExported() {
					continue
				}
				if decl.Recv != nil {
					// Methods aren't handled here
					continue
				}

				f := typesInfo.ObjectOf(decl.Name).(*types.Func)
				addStructAliasTypes(structAliasTypes, tset, tset.Normalized(f.Signature()))
				bindings = append(bindings, makeFuncBinding(f, tset))
			case *ast.GenDecl:
				switch decl.Tok {
				case token.TYPE:
					for _, spec := range decl.Specs {
						if spec, ok := spec.(*ast.TypeSpec); ok {
							if !spec.Name.IsExported() {
								continue
							}
							var namedTyp *types.Named
							var namedOrAliasTyp types.Type
							switch typ := typesInfo.ObjectOf(spec.Name).Type().(type) {
							case *types.Named:
								namedTyp = typ
								namedOrAliasTyp = namedTyp
							case *types.Alias:
								if t, ok := typ.Rhs().(*types.Named); ok && !typeutils.IsObjPublic(t.Obj()) {
									// Special case: Alias refers to a private type. We want to generate the
									// method bindings with the alias as receiver, since the alias is public.
									//
									// The counterpart in the TypeSet to this is that public aliases to private
									// types are only unaliased to the point of remaining public.
									namedTyp = t
									namedOrAliasTyp = typ
								} else {
									// Regular alias type (doesn't have any methods)
									continue
								}
							}
							if namedTyp.TypeParams() != nil {
								continue
							}
							if iface, ok := namedTyp.Underlying().(*types.Interface); ok {
								if !iface.IsMethodSet() {
									// Contains type constraints
									continue
								}
							}
							for m := range namedTyp.Methods() {
								addStructAliasTypes(structAliasTypes, tset, tset.Normalized(m.Signature()))
							}
							bindings = append(bindings, makeMethodBindings(namedOrAliasTyp, tset)...)
							namedOrAliasTypes[spec.Name.Name] = namedOrAliasTyp
						}
					}
				case token.CONST, token.VAR:
					for _, spec := range decl.Specs {
						if spec, ok := spec.(*ast.ValueSpec); ok {
							for _, name := range spec.Names {
								if !name.IsExported() {
									continue
								}
								obj := typesInfo.ObjectOf(name)
								addStructAliasTypes(structAliasTypes, tset, tset.Normalized(obj.Type()))
								bindings = append(bindings, makeGlobalGetterBinding(obj, tset))
								if decl.Tok == token.VAR {
									bindings = append(bindings, makeGlobalSetterBinding(obj, tset))
								}
							}
						}
					}
				}
			}
		}
	}

	for _, typName := range slices.Sorted(maps.Keys(namedOrAliasTypes)) {
		typ := namedOrAliasTypes[typName]

		bindings = append(bindings, makeConstructorBinding(typ, tset))
		bindings = append(bindings, makeGetUnderlyingBinding(typ, tset))
		bindings = append(bindings, makeFieldGetterBindings(typ, tset)...)
		bindings = append(bindings, makeFieldSetterBindings(typ, tset)...)
	}
	for _, name := range slices.Sorted(maps.Keys(structAliasTypes)) {
		alias := structAliasTypes[name]
		getters := makeFieldGetterBindings(alias, tset)
		setters := makeFieldSetterBindings(alias, tset)
		bindings = append(bindings, getters...)
		bindings = append(bindings, setters...)
	}

	return bindings
}
