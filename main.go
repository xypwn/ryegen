package ryegen

import (
	"cmp"
	"fmt"
	"go/ast"
	"go/token"
	"iter"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/mod/module"

	"github.com/hashicorp/go-multierror"
	"github.com/iancoleman/strcase"
	"github.com/olekukonko/tablewriter"
	"github.com/refaktor/ryegen/binder"
	"github.com/refaktor/ryegen/binder/binderio"
	"github.com/refaktor/ryegen/config"
	"github.com/refaktor/ryegen/ir"
	"github.com/refaktor/ryegen/parser"
	"github.com/refaktor/ryegen/repo"
)

func isEnvEnabled(name string) bool {
	return !slices.Contains(
		[]string{"", "0", "false", "no", "off", "disabled"},
		strings.ToLower(os.Getenv(name)),
	)
}

// modulePathElementVersion parses strings like "v2", "v3" etc.
func modulePathElementVersion(s string) int {
	if strings.HasPrefix(s, "v") {
		ver, err := strconv.Atoi(s[1:])
		if err == nil && ver >= 1 {
			return ver
		}
	}
	return -1
}

// removeModulePathVersionElements removes all "v2", "v3" etc. parts.
func removeModulePathVersionElements(s string) string {
	sp := strings.Split(s, "/")
	spOut := []string{}
	for _, v := range sp {
		if modulePathElementVersion(v) == -1 {
			spOut = append(spOut, v)
		}
	}
	return strings.Join(spOut, "/")
}

// Order of importance (descending):
// - Part of stdlib
// - Prefix of preferPkg
// - Shorter path (ignoring version numbers)
// - Smaller string according to strings.Compare (ignoring version numbers)
// - Larger version number (e.g. v2, v3)
func makeCompareModulePaths(preferPkg string) func(a, b string) int {
	return func(a, b string) int {
		aOrig, bOrig := a, b
		a, b = removeModulePathVersionElements(a), removeModulePathVersionElements(b)
		{
			aSp := strings.SplitN(a, "/", 2)
			bSp := strings.SplitN(b, "/", 2)
			if len(aSp) > 0 && len(bSp) > 0 {
				aStd := !strings.Contains(aSp[0], ".")
				bStd := !strings.Contains(bSp[0], ".")
				if aStd && !bStd {
					return -1
				} else if !aStd && bStd {
					return 1
				}
			}
		}
		if preferPkg != "" {
			aPfx := strings.HasPrefix(aOrig, preferPkg)
			bPfx := strings.HasPrefix(bOrig, preferPkg)
			if aPfx && !bPfx {
				return -1
			} else if !aPfx && bPfx {
				return 1
			}
		}
		if len(a) < len(b) {
			return -1
		} else if len(a) > len(b) {
			return 1
		}
		if a > b {
			return -1
		} else if a < b {
			return 1
		}
		{
			aSp := strings.Split(aOrig, "/")
			bSp := strings.Split(bOrig, "/")
			if len(aSp) >= 1 && len(bSp) >= 1 {
				if len(aSp) == len(bSp) &&
					modulePathElementVersion(aSp[len(aSp)-1]) > modulePathElementVersion(bSp[len(bSp)-1]) {
					return -1
				}
				if len(aSp) == len(bSp)+1 &&
					modulePathElementVersion(aSp[len(aSp)-1]) > 1 {
					return -1
				}
				if len(aSp) == len(bSp) &&
					modulePathElementVersion(aSp[len(aSp)-1]) < modulePathElementVersion(bSp[len(bSp)-1]) {
					return 1
				}
				if len(aSp)+1 == len(bSp) &&
					modulePathElementVersion(bSp[len(bSp)-1]) > 1 {
					return 1
				}
			}
		}
		return strings.Compare(aOrig, bOrig)
	}
}

func sortedMapAll[Map ~map[K]V, K cmp.Ordered, V any](m Map) iter.Seq2[K, V] {
	return func(yield func(K, V) bool) {
		ks := make([]K, 0, len(m))
		for k := range m {
			ks = append(ks, k)
		}
		slices.Sort(ks)
		for _, k := range ks {
			if !yield(k, m[k]) {
				return
			}
		}
	}
}

func sliceToSet[K cmp.Ordered](elems []K) map[K]struct{} {
	m := make(map[K]struct{}, len(elems))
	for _, elem := range elems {
		m[elem] = struct{}{}
	}
	return m
}

func recursivelyGetRepo(
	dstPath, pkg, ver string,
	onInfo func(msg string),
	excludeModules map[string]struct{},
) (
	// module path to unique (short) module name
	modUniqueNames ir.UniqueModuleNames,
	// module path to directory path
	modDirPaths map[string]string,
	// module path to name (declared in "package <name>" line)
	modDefaultNames map[string]string,
	err error,
) {
	modUniqueNames = make(ir.UniqueModuleNames)
	modDirPaths = make(map[string]string)
	modDefaultNames = make(map[string]string)

	getRepo := func(pkg, version string) (string, error) {
		have, dir, _, err := repo.Have(dstPath, pkg, version)
		if err != nil {
			return "", err
		}
		if !have {
			onInfo(fmt.Sprintf("downloading %v %v", pkg, version))
			_, err := repo.Get(dstPath, pkg, version)
			if err != nil {
				return "", err
			}
		}
		return dir, nil
	}

	srcDir, err := getRepo(pkg, ver)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("get repo: %w", err)
	}

	{
		addPkgNames := func(dir, modulePath string) (string, []module.Version, error) {
			goVer, pkgNms, req, err := parser.ParseDirModules(token.NewFileSet(), dir, modulePath, excludeModules)
			if err != nil {
				return "", nil, err
			}
			for mod, name := range pkgNms {
				if name != "" {
					modDefaultNames[mod] = name
				}
				modDirPaths[mod] = filepath.Join(dir, strings.TrimPrefix(mod, modulePath))
			}
			return goVer, req, nil
		}
		goVer, req, err := addPkgNames(srcDir, pkg)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("parse modules: %w", err)
		}
		req = append(req, module.Version{Path: "std", Version: goVer})
		for _, v := range req {
			dir, err := getRepo(v.Path, v.Version)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("get repo: %w", err)
			}
			if _, _, err := addPkgNames(dir, v.Path); err != nil {
				return nil, nil, nil, fmt.Errorf("parse modules: %w", err)
			}
		}
	}
	modUniqueNames["C"] = "C"
	{
		moduleNameKeys := make([]string, 0, len(modDefaultNames))
		for k := range modDefaultNames {
			moduleNameKeys = append(moduleNameKeys, k)
		}
		slices.SortFunc(moduleNameKeys, makeCompareModulePaths(pkg))

		existingModuleNames := make(map[string]struct{})
		for _, modPath := range moduleNameKeys {
			// Create a unique module path. If the default name as declared in the
			// "package <name>" directive doesn't work, try prepending the previous
			// element of the path.
			// Does not repeat name components, or include version numbers like "v2".
			// Example:
			// 	modPath = github.com/username/reponame/resources/audio
			//  "audio" is already taken.
			//  Try "resources_audio".
			//  If that's already taken, try "reponame_resources_audio" etc.

			modPathElems := strings.Split(removeModulePathVersionElements(modPath), "/")
			nameComponents := []string{modDefaultNames[modPath]}
			for ; func() bool {
				_, exists := existingModuleNames[strings.Join(nameComponents, "_")]
				return exists
			}(); modPathElems = modPathElems[:len(modPathElems)-1] {
				if len(modPathElems) == 0 {
					return nil, nil, nil, fmt.Errorf("cannot create unique module name for %v", modPath)
				}

				lastElem := modPathElems[len(modPathElems)-1]
				lastElemSnakeCase := strcase.ToSnake(lastElem)
				if slices.Contains(nameComponents, lastElemSnakeCase) {
					continue
				}

				nameComponents = append([]string{lastElemSnakeCase}, nameComponents...)
			}
			name := strings.Join(nameComponents, "_")
			modUniqueNames[modPath] = name
			existingModuleNames[name] = struct{}{}
		}
	}

	return
}

// May return a *multierror.Error in err, in which case the error
// is non-fatal.
func parsePkgs(
	pkgDlPath string,
	pkgs []string,
	modUniqueNames ir.UniqueModuleNames,
	modDirPaths map[string]string,
	modDefaultNames map[string]string,
	excludeModules map[string]struct{},
) (
	irData *ir.IR,
	genBindingsForPkgs []string,
	err error,
) {
	var resErr error

	var fileInfo []ir.IRInputFileInfo
	genBindPkgs := make(map[string]struct{}) // mod paths

	parseDirGo := func(dirPath string, modulePath string) error {
		pkgs, err := parser.ParseDir(token.NewFileSet(), dirPath, modulePath, -1, excludeModules)
		if err != nil {
			return err
		}

		for _, pkg := range pkgs {
			for name, f := range pkg.Files {
				name := strings.TrimPrefix(name, pkgDlPath+string(filepath.Separator))
				fileInfo = append(fileInfo, ir.IRInputFileInfo{
					File:       f,
					Name:       name,
					ModulePath: pkg.Path,
				})
			}
			genBindPkgs[pkg.Path] = struct{}{}
		}
		return nil
	}

	slices.SortFunc(fileInfo, func(a ir.IRInputFileInfo, b ir.IRInputFileInfo) int {
		return strings.Compare(a.Name, b.Name)
	})

	for _, pkg := range pkgs {
		dirPath, ok := modDirPaths[pkg]
		if !ok {
			return nil, nil, fmt.Errorf("unknown package: %v", pkg)
		}
		if err := parseDirGo(dirPath, pkg); err != nil {
			return nil, nil, err
		}
	}

	irData, err = ir.Parse(
		modUniqueNames,
		modDefaultNames,
		fileInfo,
		func(modulePath string) (map[string]*ast.File, error) {
			dirPath, ok := modDirPaths[modulePath]
			if !ok {
				return nil, fmt.Errorf("unknown package: %v", modulePath)
			}
			pkgs, err := parser.ParseDir(token.NewFileSet(), dirPath, modulePath, 1, excludeModules)
			if err != nil {
				return nil, err
			}

			res := make(map[string]*ast.File)
			for _, pkg := range pkgs {
				for name, f := range pkg.Files {
					name := strings.TrimPrefix(name, pkgDlPath+string(filepath.Separator))
					if _, ok := res[name]; ok {
						return nil, fmt.Errorf("getDependency: duplicate file name %v in package %v", name, pkg.Name)
					}
					res[name] = f
				}
			}
			return res, nil
		},
	)
	if err != nil {
		if multErr, ok := err.(*multierror.Error); ok {
			resErr = multierror.Append(resErr, multErr.Errors...)
		} else {
			return nil, nil, err
		}
	}

	return irData, slices.Sorted(maps.Keys(genBindPkgs)), resErr
}

// May return a *multierror.Error in resErr, in which case the error
// is non-fatal.
func genBindings(
	targetPkgs []string,
	ctx *binder.Context,
) (
	bindings []*binder.BindingFunc,
	genericInterfaceImpls []string,
	deps *binder.Dependencies,
	resErr error,
) {
	deps = binder.NewDependencies()

	for _, iface := range sortedMapAll(ctx.IR.Interfaces) {
		if iface.Name.File == nil || ir.IdentIsInternal(ctx.ModNames, iface.Name) {
			continue
		}
		if !slices.Contains(targetPkgs, iface.Name.File.ModulePath) {
			continue
		}
		for _, fn := range iface.Funcs {
			bind, err := binder.GenerateBinding(deps, ctx, fn)
			if err != nil {
				resErr = multierror.Append(resErr, fmt.Errorf("%v: %w", fn.String(), err))
				continue
			}
			bindings = append(bindings, bind)
		}
	}

	for _, fn := range sortedMapAll(ctx.IR.Funcs) {
		if ir.ModulePathIsInternal(ctx.ModNames, fn.File.ModulePath) || (fn.Recv != nil && ir.IdentIsInternal(ctx.ModNames, *fn.Recv)) {
			continue
		}
		if !slices.Contains(targetPkgs, fn.File.ModulePath) {
			continue
		}
		bind, err := binder.GenerateBinding(deps, ctx, fn)
		if err != nil {
			resErr = multierror.Append(resErr, fmt.Errorf("%v: %w", fn.String(), err))
			continue
		}
		bindings = append(bindings, bind)
	}

	for _, struc := range sortedMapAll(ctx.IR.Structs) {
		if struc.Name.File == nil || ir.IdentIsInternal(ctx.ModNames, struc.Name) {
			continue
		}
		if !slices.Contains(targetPkgs, struc.Name.File.ModulePath) {
			continue
		}
		for _, f := range struc.Fields {
			for _, setter := range []bool{false, true} {
				bind, err := binder.GenerateGetterOrSetter(deps, ctx, f, struc.Name, setter)
				if err != nil {
					s := struc.Name.Name + "//" + f.Name.Name
					if setter {
						s += "!"
					} else {
						s += "?"
					}
					resErr = multierror.Append(resErr, fmt.Errorf("%v: %w", s, err))
					continue
				}
				bindings = append(bindings, bind)
			}
		}
	}

	for _, value := range sortedMapAll(ctx.IR.Values) {
		if value.Name.File == nil || ir.IdentIsInternal(ctx.ModNames, value.Name) {
			continue
		}
		if !slices.Contains(targetPkgs, value.Name.File.ModulePath) {
			continue
		}
		bind, err := binder.GenerateValue(deps, ctx, value)
		if err != nil {
			s := value.Name.Name
			resErr = multierror.Append(resErr, fmt.Errorf("%v: %w", s, err))
			continue
		}
		bindings = append(bindings, bind)
	}

	for _, struc := range sortedMapAll(ctx.IR.Structs) {
		if struc.Name.File == nil || ir.IdentIsInternal(ctx.ModNames, struc.Name) {
			continue
		}
		if !slices.Contains(targetPkgs, struc.Name.File.ModulePath) {
			continue
		}
		bind, err := binder.GenerateNewStruct(deps, ctx, struc.Name)
		if err != nil {
			s := struc.Name.Name
			resErr = multierror.Append(resErr, fmt.Errorf("%v: %w", s, err))
			continue
		}
		if !slices.ContainsFunc(bindings, func(b *binder.BindingFunc) bool {
			return b.UniqueName(ctx) == bind.UniqueName(ctx)
		}) {
			// Only generate NewMyStruct if the function doesn't already exist.
			bindings = append(bindings, bind)
		}
	}

	genericIfaceImpls := make(map[string]string)
	for {
		// Generate interface impls recursively until all are implemented,
		// since generating one might cause another one to be required
		addedImpl := false
		for name, iface := range sortedMapAll(deps.GenericInterfaceImpls) {
			if _, ok := genericIfaceImpls[name]; ok {
				continue
			}
			ifaceImpl, err := binder.GenerateGenericInterfaceImpl(deps, ctx, iface)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("generate generic interface impl: %w", err)
			}
			addedImpl = true
			rep := strings.NewReplacer(`((RYEGEN:FUNCNAME))`, "context to "+iface.Name.Name)
			genericIfaceImpls[name] = rep.Replace(ifaceImpl)
		}
		if !addedImpl {
			break
		}
	}
	genericInterfaceImpls = slices.Collect(maps.Values(genericIfaceImpls))

	return
}

func TryRun(
	onInfo func(msg string),
) (
	outFile string,
	stats string,
	warn error,
	err error,
) {
	var cfg *config.Config
	{
		const configPath = "config.toml"
		var createdDefault bool
		var err error
		cfg, createdDefault, err = config.ReadConfigFromFileOrCreateDefault(configPath)
		if err != nil {
			return "", "", nil, fmt.Errorf("open config: %w", err)
		}
		if createdDefault {
			return "", "", fmt.Errorf("created default config at %v", configPath), nil
		}
	}

	excludeModules := sliceToSet(cfg.Exclude)

	const pkgDlPath = "_srcrepos"

	timeStart := time.Now()

	modUniqueNames,
		modDirPaths,
		modDefaultNames,
		err := recursivelyGetRepo(pkgDlPath, cfg.Package, cfg.Version, onInfo, excludeModules)
	if err != nil {
		return "", "", nil, fmt.Errorf("get repo: %w", err)
	}

	timeGetRepos := time.Since(timeStart)
	timeStart = time.Now()

	irData, genBindingsForPkgs, err := parsePkgs(
		pkgDlPath,
		append([]string{cfg.Package}, cfg.IncludeStdLibs...),
		modUniqueNames,
		modDirPaths,
		modDefaultNames,
		excludeModules,
	)
	if err != nil {
		return "", "", nil, fmt.Errorf("parse packages: %w", err)
	}

	timeParse := time.Since(timeStart)
	timeStart = time.Now()

	ctx := binder.NewContext(cfg, irData, modUniqueNames)

	bindings, genericInterfaceImpls, dependencies, err := genBindings(genBindingsForPkgs, ctx)
	if err != nil {
		if multErr, ok := err.(*multierror.Error); ok {
			warn = multierror.Append(warn, multErr.Errors...)
		} else {
			return "", "", nil, fmt.Errorf("generate bindings: %w", err)
		}
	}

	timeGenBindings := time.Since(timeStart)
	timeStart = time.Now()

	const bindingListPath = "bindings.txt"
	var bindingList *config.BindingList
	if _, err := os.Stat(bindingListPath); err == nil {
		var err error
		bindingList, err = config.LoadBindingListFromFile(bindingListPath)
		if err != nil {
			return "", "", nil, err
		}
	} else {
		bindingList = config.NewBindingList()
	}
	{
		bindingFuncsToDocstrs := make(map[string]string, len(bindings))
		for _, bind := range bindings {
			bindingFuncsToDocstrs[bind.UniqueName(ctx)] = bind.Doc
		}
		if err := bindingList.SaveToFile(bindingListPath, bindingFuncsToDocstrs); err != nil {
			return "", "", nil, err
		}
	}

	timeReadWriteBindingsTXT := time.Since(timeStart)
	timeStart = time.Now()

	// Default dependencies (document all usage for each)
	dependencies.Imports["github.com/refaktor/rye/env"] = struct{}{}    // force-used and not tracked
	dependencies.Imports["github.com/refaktor/rye/evaldo"] = struct{}{} // force-used and not tracked
	dependencies.Imports["reflect"] = struct{}{}                        // ifaceToNative
	dependencies.Imports["strings"] = struct{}{}                        // builtinsPreset: "kind"

	var fullBindingName string
	{
		var b strings.Builder
		for _, r := range cfg.Package {
			r = unicode.ToLower(r)
			if (r < 'a' || r > 'z') &&
				(r < '0' || r > '9') {
				r = '_'
			}
			b.WriteRune(r)
		}
		fullBindingName = b.String()
	}

	outDir := filepath.Join(cfg.OutDir, fullBindingName)
	if err := os.MkdirAll(outDir, os.ModePerm); err != nil {
		return "", "", nil, err
	}
	outFileCustom := filepath.Join(outDir, "custom.go")
	outFileNot := filepath.Join(outDir, "generated.not.go")
	outFile = filepath.Join(outDir, "generated.go")

	if _, err := os.Stat(outFileCustom); os.IsNotExist(err) {
		var cb binderio.CodeBuilder
		cb.Append(`// Add your custom builtins to this file.

package ` + fullBindingName + `

import (
	"github.com/refaktor/rye/env"
)

var builtinsCustom = map[string]*env.Builtin{
	// Add your custom builtins here:
}
`)
		if fmtErr, err := cb.SaveToFile(outFileCustom); err != nil || fmtErr != nil {
			return "", "", nil, fmt.Errorf("save custom.go: general=%w, fmt=%v", err, fmtErr)
		}
	} else if err != nil {
		return "", "", nil, fmt.Errorf("stat custom.go: %w", err)
	}

	if cfg.DontBuildFlag == "" {
		if _, err := os.Stat(outFileNot); err == nil {
			if err := os.Remove(outFileNot); err != nil {
				return "", "", nil, fmt.Errorf("remove %v: %w", outFileNot, err)
			}
		}
	} else {
		var cb binderio.CodeBuilder
		cb.Append(`// Code generated by ryegen. DO NOT EDIT.

//go:build ` + cfg.DontBuildFlag + `

package ` + fullBindingName + `

import "github.com/refaktor/rye/env"

var Builtins = map[string]*env.Builtin{}
`)
		if fmtErr, err := cb.SaveToFile(outFileNot); err != nil || fmtErr != nil {
			return "", "", nil, fmt.Errorf("save binding dummy: general=%w, fmt=%v", err, fmtErr)
		}
	}

	var cb binderio.CodeBuilder

	cb.Linef(`// Code generated by ryegen. DO NOT EDIT.`)
	cb.Linef(``)
	cb.Linef(`// You can add custom binding code to custom.go!`)
	cb.Linef(``)
	if cfg.DontBuildFlag != "" {
		cb.Linef(`//go:build !%v`, cfg.DontBuildFlag)
		cb.Linef(``)
	}
	cb.Linef(`package %v`, fullBindingName)
	cb.Linef(``)
	cb.Linef(`import (`)
	cb.Indent++
	for _, mod := range slices.Sorted(maps.Keys(dependencies.Imports)) {
		defaultName := modDefaultNames[mod]
		uniqueName := ctx.ModNames[mod]
		if defaultName == uniqueName {
			cb.Linef(`"%v"`, mod)
		} else {
			cb.Linef(`%v "%v"`, uniqueName, mod)
		}
	}
	cb.Indent--
	cb.Linef(`)`)

	cb.Append(`
var Builtins map[string]*env.Builtin

func init() {
	Builtins = make(map[string]*env.Builtin, len(builtinsGenerated)+len(builtinsCustom))
	for k, v := range builtinsPreset {
		Builtins[k] = v
	}
	for k, v := range builtinsGenerated {
		Builtins[k] = v
	}
	for k, v := range builtinsCustom {
		Builtins[k] = v
	}
}

// Force-use evaldo and env packages since tracking them would be too complicated
var _ = evaldo.BuiltinNames
var _ = env.Object(nil)

func boolToInt64(x bool) int64 {
	var res int64
	if x {
		res = 1
	}
	return res
}

func objectDebugString(idx *env.Idxs, v any) string {
	if v, ok := v.(env.Object); ok {
		return v.Inspect(*idx)
	} else {
		return "[Non-object of type " + reflect.TypeOf(v).String() + "]"
	}
}

func ifaceToNative(idx *env.Idxs, v any, ifaceName string) env.Native {
	rV := reflect.ValueOf(v)
	var typRyeName string
	var ok bool
	if rV.Type() != nil {
		var typPfx string
		if rV.Type().Kind() == reflect.Struct {
			newRV := reflect.New(rV.Type())
			newRV.Elem().Set(rV)
			rV = newRV
		}
		typ := rV.Type()
		if typ.Kind() == reflect.Pointer {
			typ = rV.Type().Elem()
			typPfx = "*"
		}
		typRyeName, ok = ryeStructNameLookup[typ.PkgPath()+"."+typPfx+typ.Name()]
	}
	if ok {
		return *env.NewNative(idx, rV.Interface(), typRyeName)
	} else {
		return *env.NewNative(idx, rV.Interface(), ifaceName)
	}
}

var builtinsPreset = map[string]*env.Builtin{
	"nil": {
		Doc: "nil value for go types",
		Fn: func(ps *env.ProgramState, arg0, arg1, arg2, arg3, arg4 env.Object) env.Object {
			return *env.NewInteger(0)
		},
	},
	"kind": {
		Doc: "underlying kind of a go native",
		Fn: func(ps *env.ProgramState, arg0, arg1, arg2, arg3, arg4 env.Object) env.Object {
			nat, ok := arg0.(env.Native)
			if !ok {
				ps.FailureFlag = true
				return env.NewError("kind: arg0: expected native")
			}
			s := ps.Idx.GetWord(nat.Kind.Index)
			s = s[3 : len(s)-1]            // remove surrounding "Go()"
			s = strings.TrimPrefix(s, "*") // remove potential pointer "*"
			return *env.NewString(s)
		},
	},
}

`)

	cb.Linef(`var ryeStructNameLookup = map[string]string{`)
	cb.Indent++
	{
		typNames := make(map[string]string, len(irData.Structs)*2)
		for _, struc := range irData.Structs {
			id := struc.Name
			if !ir.IdentExprIsExported(id.Expr) || ir.IdentIsInternal(ctx.ModNames, id) {
				continue
			}
			var nameNoMod string
			switch expr := id.Expr.(type) {
			case *ast.Ident:
				nameNoMod = expr.Name
			case *ast.StarExpr:
				id, ok := expr.X.(*ast.Ident)
				if !ok {
					continue
				}
				nameNoMod = "*" + id.Name
			case *ast.SelectorExpr:
				nameNoMod = expr.Sel.Name
			default:
				continue
			}

			var err error
			id, err = ir.NewIdent(ctx.IR.ConstValues, ctx.ModNames, id.File, &ast.StarExpr{X: id.Expr})
			if err != nil {
				panic(err)
			}

			typNames[id.File.ModulePath+".*"+nameNoMod] = id.RyeName()
		}
		for k, v := range sortedMapAll(typNames) {
			cb.Linef(`"%v": "%v",`, k, v)
		}
	}
	cb.Indent--
	cb.Linef(`}`)
	cb.Linef(``)

	for _, ifaceImpl := range slices.Sorted(slices.Values(genericInterfaceImpls)) {
		cb.Append(ifaceImpl)
	}

	sortedBindings := slices.SortedFunc(slices.Values(bindings), func(bf1, bf2 *binder.BindingFunc) int {
		return strings.Compare(bf1.UniqueName(ctx), bf2.UniqueName(ctx))
	})

	bindingNames := make([]string, len(sortedBindings))
	{
		namePrios := make([]int, len(sortedBindings))
		for i, bind := range sortedBindings {
			prio := slices.Index(cfg.NoPrefix, bind.File.ModulePath)
			if prio == -1 {
				prio = math.MaxInt
			}
			namePrios[i] = prio
		}
		nameCandidates := make([][]string, len(sortedBindings))
		for i, bind := range sortedBindings {
			nameCandidates[i] = bind.RyeifiedNameCandidates(ctx, namePrios[i] != math.MaxInt, cfg.CutNew, bindingList.Renames[bind.UniqueName(ctx)])
		}
		for {
			foundConflict := false
			topNames := make(map[string]int) // current top candidate to index into sortedBindings
			for i, bind := range sortedBindings {
				if len(nameCandidates[i]) == 0 {
					return "", "", nil, fmt.Errorf("unable to resolve naming conflict for %v", bind.UniqueName(ctx))
				}
				topName := nameCandidates[i][0]
				if otherI, exists := topNames[topName]; exists {
					if namePrios[otherI] < namePrios[i] /* lower means higher priority (in this case otherI has higher priority) */ {
						nameCandidates[i] = nameCandidates[i][1:]
						topNames[topName] = otherI
						foundConflict = true
					} else if namePrios[i] < namePrios[otherI] /* i has higher priority than otherI */ {
						nameCandidates[otherI] = nameCandidates[otherI][1:]
						topNames[topName] = i
						foundConflict = true
					} else {
						// TODO: Find a better way to do this.
						warn = multierror.Append(warn,
							fmt.Errorf(
								"unable to resolve naming conflict between %v and %v, renaming %v to %v",
								bind.UniqueName(ctx), sortedBindings[otherI].UniqueName(ctx),
								nameCandidates[i][0], nameCandidates[i][0]+"-1",
							),
						)
						nameCandidates[i][0] += "-1"
						topName = nameCandidates[i][0]
						topNames[topName] = i
						foundConflict = true
					}
				} else {
					topNames[topName] = i
				}
			}
			if !foundConflict {
				// no conflicts left
				break
			}
		}
		for i := range sortedBindings {
			bindingNames[i] = nameCandidates[i][0]
		}
	}

	for i, bind := range sortedBindings {
		if _, ok := bindingList.Export[bind.UniqueName(ctx)]; !ok {
			continue
		}
		funcName := strcase.ToSnake(bindingNames[i])
		cb.Linef(`func ExportedFunc_%v(funcName string, ps *env.ProgramState, arg0, arg1, arg2, arg3, arg4 env.Object) env.Object {`, funcName)
		cb.Indent++
		rep := strings.NewReplacer(`((RYEGEN:FUNCNAME))`, `" + funcName + "`)
		cb.Append(rep.Replace(bind.Body))
		cb.Indent--
		cb.Linef(`}`)
		cb.Linef(``)
	}

	cb.Linef(`var builtinsGenerated = map[string]*env.Builtin{`)
	cb.Indent++

	numWrittenBindings := 0
	numBindingsByCategory := make(map[string]int)
	numWrittenBindingsByCategory := make(map[string]int)
	for i, bind := range sortedBindings {
		numBindingsByCategory[bind.Category]++
		if enabled, ok := bindingList.Enabled[bind.UniqueName(ctx)]; ok && !enabled {
			continue
		}
		if bind.DocComment != "" {
			lines := strings.Split(bind.DocComment, "\n")
			if lines[len(lines)-1] == "" {
				lines = lines[:len(lines)-1]
			}
			for _, line := range lines {
				name := bindingNames[i]
				if _, s, ok := strings.Cut(name, "//"); ok {
					name = s
				}
				line = strings.ReplaceAll(line, bind.Name, name)
				cb.Linef(`// %v`, line)
			}
		}
		cb.Linef(`"%v": {`, bindingNames[i])
		cb.Indent++
		cb.Linef(`Doc: "%v",`, bind.Doc)
		cb.Linef(`Argsn: %v,`, bind.Argsn)
		cb.Linef(`Fn: func(ps *env.ProgramState, arg0, arg1, arg2, arg3, arg4 env.Object) env.Object {`)
		cb.Indent++
		rep := strings.NewReplacer(`((RYEGEN:FUNCNAME))`, bindingNames[i])
		cb.Append(rep.Replace(bind.Body))
		cb.Indent--
		cb.Linef(`},`)
		cb.Indent--
		cb.Linef(`},`)
		numWrittenBindingsByCategory[bind.Category]++
		numWrittenBindings++
	}

	cb.Indent--
	cb.Linef(`}`)

	{
		fmtErr, err := cb.SaveToFile(outFile)
		if err != nil {
			return "", "", nil, fmt.Errorf("save bindings: %w", err)
		}
		if fmtErr != nil {
			warn = multierror.Append(warn, fmt.Errorf("cannot format bindings: %w, saved as unformatted go code instead", fmtErr))
		}
	}

	timeWriteCode := time.Since(timeStart)

	{
		var sw strings.Builder
		fmt.Fprintf(&sw, "==Binding stats==\n")
		fmt.Fprintf(&sw, "Generated %v generic interface implementations.\n", len(genericInterfaceImpls))
		fmt.Fprintf(&sw, "Number of generated builtins (excludes generic interface impls):\n")
		{
			tbl := tablewriter.NewWriter(&sw)
			tbl.SetHeader([]string{"Category", "Written/Total"})
			for _, cat := range slices.Sorted(maps.Keys(numBindingsByCategory)) {
				tbl.Append([]string{cat, fmt.Sprintf("%v/%v", numWrittenBindingsByCategory[cat], numBindingsByCategory[cat])})
			}
			tbl.Append([]string{"==TOTAL==", fmt.Sprintf("%v/%v", numWrittenBindings, len(bindings))})
			tbl.SetColumnAlignment([]int{tablewriter.ALIGN_LEFT, tablewriter.ALIGN_CENTER})
			tbl.SetBorders(tablewriter.Border{Left: true, Top: false, Right: true, Bottom: false})
			tbl.SetCenterSeparator("|")
			tbl.Render()
		}
		fmt.Fprintln(&sw)
		fmt.Fprintf(&sw, "==Timing stats==\n")
		fmt.Fprintf(&sw, "Fetched/checked source repos in %v.\n", timeGetRepos)
		fmt.Fprintf(&sw, "Binding generation tasks (excludes fetching/checking source repos):\n")
		{
			timeTotal := timeParse + timeGenBindings + timeReadWriteBindingsTXT + timeWriteCode
			timePercent := func(t time.Duration) string {
				return strconv.FormatFloat(
					float64(t)/float64(timeTotal)*100,
					'f', 2, 64,
				)
			}

			tbl := tablewriter.NewWriter(&sw)
			tbl.SetHeader([]string{"Task", "Time", "Time %"})
			tbl.AppendBulk([][]string{
				{"Parse", timeParse.String(), timePercent(timeParse)},
				{"Generate bindings", timeGenBindings.String(), timePercent(timeGenBindings)},
				{"Read/Write bindings.txt", timeReadWriteBindingsTXT.String(), timePercent(timeReadWriteBindingsTXT)},
				{"Write and format code", timeWriteCode.String(), timePercent(timeWriteCode)},
				{"==TOTAL==", timeTotal.String(), "100"},
			})
			tbl.SetColumnAlignment([]int{tablewriter.ALIGN_LEFT, tablewriter.ALIGN_CENTER, tablewriter.ALIGN_RIGHT})
			tbl.SetBorders(tablewriter.Border{Left: true, Top: false, Right: true, Bottom: false})
			tbl.SetCenterSeparator("|")
			tbl.Render()
		}
		stats = sw.String()
	}

	return outFile, stats, warn, nil
}

func Run() {
	outFile, stats, warn, err := TryRun(func(msg string) {
		fmt.Println("Ryegen:", msg)
	})
	if err != nil {
		fmt.Println("Ryegen: fatal:", err)
		os.Exit(1)
	}
	if isEnvEnabled("RYEGEN_STATS") {
		fmt.Println()
		fmt.Println("====== BEGIN RYEGEN STATS ======")
		fmt.Println()
		fmt.Println(stats)
		fmt.Println("======  END RYEGEN STATS  ======")
		fmt.Println()
	}
	if warn != nil {
		if multErr, ok := warn.(*multierror.Error); ok {
			fmt.Println("Ryegen:", len(multErr.Errors), "warnings:")
			for _, e := range multErr.Errors {
				fmt.Println("  *", e)
			}
		} else {
			fmt.Println("Ryegen: warning:", warn)
		}
	}
	fmt.Println("Ryegen: Wrote bindings to", outFile)
}
