var pkgLookup = make(map[string]string, 0)
func conv_error_toRye(ps *_env.ProgramState, ctx *_env.RyeCtx, x error) (_env.Object, error) {
	if x == nil {
		return *_env.NewVoid(), nil
	}
	return *_env.NewError(x.Error()), nil
}