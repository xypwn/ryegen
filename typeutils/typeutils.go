package typeutils

import (
	"go/types"
	"maps"
	"slices"
	"strings"

	"github.com/refaktor/ryegen/v2/converter/walktypes"
)

func CollectImports(t types.Type) []*types.Package {
	imports := map[string]*types.Package{}
	var doCollectImports func(t types.Type)
	doCollectImports = func(t types.Type) {
		switch t := t.(type) {
		case *types.Named:
			if t.Obj().Exported() {
				if pkg := t.Obj().Pkg(); pkg != nil {
					imports[pkg.Path()] = pkg
				}
			}
		case *types.Basic:
			if t.Kind() == types.UnsafePointer {
				imports["unsafe"] = types.Unsafe
			}
		}
		walktypes.Walk(t, doCollectImports)
	}
	doCollectImports(t)
	return slices.Collect(maps.Values(imports))
}

// IsPkgPathInternal returns whether the given package
// is internal, meaning it cannot be imported from a
// different module.
func IsPkgPathInternal(pkg *types.Package) bool {
	if pkg != nil {
		for sp := range strings.SplitSeq(pkg.Path(), "/") {
			if sp == "internal" {
				return true
			}
		}
	}
	return false
}

// IsObjPublic returns true exactly if the object is EITHER:
// 1. in the universe scope 2. exported and not internal.
func IsObjPublic(obj types.Object) bool {
	return obj.Pkg().Scope() == types.Universe || (obj.Exported() && !IsPkgPathInternal(obj.Pkg()))
}
