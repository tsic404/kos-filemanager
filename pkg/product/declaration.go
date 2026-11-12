package product

// App returns the `App` declaration of KOSD-C-73 as a plain JSON object. The
// SDK validates the field set; building it as a map keeps the wire shape the
// contract fixes rather than a Go type's zero values.
//
// entry is the window's document address on its own origin; the host reads its
// path as the package asset the frame loads (KOSD-C-73).
func App(entry string) map[string]any {
	return map[string]any{
		"id":      AppID,
		"name":    AppName,
		"version": Version,
		"icon":    IconAsset,
		"entry":   entry,
		"window": map[string]any{
			"width":        WindowWidth,
			"height":       WindowHeight,
			"minWidth":     WindowMinWidth,
			"minHeight":    WindowMinHeight,
			"resizable":    WindowResizable,
			"mode":         WindowModeSingle,
			"defaultState": WindowDefaultState,
		},
		"permissions": permissionNames(),
		"backend": map[string]any{
			"type":   BackendType,
			"health": BackendHealth(entry),
		},
	}
}

// Declaration returns the extension declaration of KOSD-C-18 in the field set
// the framework fixes: the window application, the preference structure its
// settings surface edits, the read scope the data plane needs on volumes, and
// the fate of the preference data when the component is uninstalled.
//
// The exposed-volume reference is a preference, not a grant: the data plane
// authorizes by volume owner (KOSD-C-60, FILE-C-1), so no setting widens it.
func Declaration(entry string) map[string]any {
	return map[string]any{
		"identifier":       Identifier,
		"version":          Version,
		"frameworkVersion": FrameworkVersion,
		"contributions": map[string]any{
			"preferences": []any{
				map[string]any{"id": PreferenceDomain, "keys": anyPreferences()},
			},
			"apps": []any{App(entry)},
		},
		"dependencies": map[string]any{},
		"permissions": []any{
			map[string]any{
				"contribution": AppID,
				"readScope": map[string]any{
					"groups": []any{VolumeAPIGroup},
					"kinds":  []any{map[string]any{"group": VolumeAPIGroup, "kind": VolumeKind}},
				},
			},
		},
		"configuration": map[string]any{},
		"retention": []any{
			map[string]any{"resource": "preferences", "keep": false},
		},
	}
}

// BackendHealth is the absolute health address of the declared backend: the
// entry's origin plus the path this service answers (KOSD-C-73).
func BackendHealth(entry string) string {
	origin, err := Origin(entry)
	if err != nil {
		return ""
	}
	return origin + HealthPath
}

// permissionNames renders the fixed capability set in its wire form.
func permissionNames() []any {
	names := make([]any, 0, len(Permissions))
	for _, permission := range Permissions {
		names = append(names, string(permission))
	}
	return names
}

// anyPreferences renders the declared preference keys in their wire form.
func anyPreferences() []any {
	keys := make([]any, 0, len(PreferenceKeys))
	for _, key := range PreferenceKeys {
		keys = append(keys, key)
	}
	return keys
}
