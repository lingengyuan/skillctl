package main

import (
	"encoding/json/v2"
	"fmt"
	"os"
	"slices"
)

// Provider records remain authoritative. Removing their actual installation
// removes the matching record in the same transaction; detaching an Agent link
// leaves the source record and shared directory intact.
func planRemovedProviderRecords(plan *lifecyclePlan, view *inventoryView, removed []skill) error {
	provenance, _ := newProvenanceIndex(view.skills, view.state, view.manifests, view.managed)
	manifests := map[string]map[string]any{}
	stateChanged := false
	for _, item := range removed {
		claims := provenance.claims(item)
		if claims.count() > 1 {
			return fmt.Errorf("conflicting ownership prevents removal of %s", item.Name)
		}
		if claims.hasVercel {
			path := claims.vercel.ManifestPath
			object := manifests[path]
			if object == nil {
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				if err := json.Unmarshal(data, &object); err != nil {
					return err
				}
				manifests[path] = object
			}
			entries, ok := object["skills"].(map[string]any)
			if !ok {
				return fmt.Errorf("unsupported provider lock shape")
			}
			delete(entries, claims.vercel.Name)
		}
		if claims.hasTracked {
			before := len(view.state.Skills)
			view.state.Skills = slices.DeleteFunc(view.state.Skills, func(entry trackedEntry) bool { return samePath(entry.Path, claims.tracked.Path) })
			stateChanged = stateChanged || before != len(view.state.Skills)
		}
	}
	for path, object := range manifests {
		data, err := json.Marshal(object)
		if err != nil {
			return err
		}
		if err := addFileIfChanged(plan, path, append(data, '\n')); err != nil {
			return err
		}
	}
	if stateChanged {
		data, err := trackedStateBytes(view.state)
		if err != nil {
			return err
		}
		if err := addFileIfChanged(plan, view.state.path, data); err != nil {
			return err
		}
	}
	return nil
}
