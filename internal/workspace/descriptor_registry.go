package workspace

import (
	"os"
	"path/filepath"
	"strings"
)

func rejectShortAliasInRoot(workspaceRoot, rel string) error {
	if workspaceRoot == "" || rel == "" || !strings.ContainsRune(rel, '~') {
		return nil
	}
	parts := strings.Split(rel, "/")
	cur := workspaceRoot
	for _, part := range parts {
		entries, err := os.ReadDir(cur)
		if err != nil {
			return nil
		}
		if strings.ContainsRune(part, '~') {
			matchedLong := false
			for _, e := range entries {
				if strings.EqualFold(e.Name(), part) {
					matchedLong = true
					break
				}
			}
			if !matchedLong {
				if _, statErr := os.Lstat(filepath.Join(cur, part)); statErr == nil {
					return fail("invalid_path")
				}
			}
		}
		cur = filepath.Join(cur, part)
	}
	return nil
}

func defaultRootID(root string) string {
	if root == "" {
		return DefaultRoot
	}
	return root
}

func singleReadTarget(in DescriptorInput, rootAllowed bool) (string, error) {
	raw := in.Path
	if raw == "" && len(in.Targets) == 1 && !in.Targets[0].Remove {
		raw = in.Targets[0].Path
	} else if len(in.Targets) > 0 {
		return "", fail("batch_limit")
	}
	rel, err := relative(raw, rootAllowed)
	if err != nil {
		return "", err
	}
	if err := rejectShortAliasInRoot(in.WorkspaceRoot, rel); err != nil {
		return "", err
	}
	return rel, nil
}

func prepareMutatingTargets(in DescriptorInput, maxTargets int, allowRemove bool) ([]Target, []ResourceClaim, error) {
	if len(in.Targets) == 0 || len(in.Targets) > maxTargets {
		return nil, nil, fail("batch_limit")
	}
	root := defaultRootID(in.RootID)
	seen := map[string]bool{}
	normalized := make([]Target, 0, len(in.Targets))
	var claims []ResourceClaim
	touchesMemory := false
	for _, t := range in.Targets {
		if t.Remove && !allowRemove {
			return nil, nil, fail("undeclared_effect")
		}
		rel, err := relative(t.Path, false)
		if err != nil {
			return nil, nil, err
		}
		if err := rejectShortAliasInRoot(in.WorkspaceRoot, rel); err != nil {
			return nil, nil, err
		}
		k := pathKey(rel)
		if seen[k] {
			return nil, nil, fail("duplicate_target")
		}
		seen[k] = true
		t.Path = rel
		normalized = append(normalized, t)
		claims = append(claims, ResourceClaim{Domain: DomainFS, Root: root, Key: k, Coverage: CoverageExact, Access: AccessExclusive})
		if IsMemorySourcePath(rel) {
			touchesMemory = true
		}
		if !t.Remove {
			for _, anc := range missingAncestorKeys(in.WorkspaceRoot, rel) {
				claims = append(claims, ResourceClaim{Domain: DomainFS, Root: root, Key: anc, Coverage: CoverageExact, Access: AccessExclusive})
				if IsMemorySourcePath(anc) {
					touchesMemory = true
				}
			}
		}
	}
	if touchesMemory {
		claims = append(claims, ResourceClaim{Domain: DomainMemorySources, Root: root, Key: "sources", Coverage: CoverageExact, Access: AccessExclusive})
	}
	merged, err := dedupClaims(claims)
	return normalized, merged, err
}

var descriptorTable = buildDescriptorTable()

func LookupDescriptor(name string) (ToolDescriptor, bool) {
	d, ok := descriptorTable[name]
	return d, ok
}

func RegisteredDescriptors() map[string]ToolDescriptor {
	out := make(map[string]ToolDescriptor, len(descriptorTable))
	for k, v := range descriptorTable {
		out[k] = v
	}
	return out
}

func PrepareTool(name string, in DescriptorInput) (PreparedPlan, error) {
	d, ok := descriptorTable[name]
	if !ok {
		return PreparedPlan{}, fail("unsupported_tool")
	}
	return d.Prepare(in)
}

func buildDescriptorTable() map[string]ToolDescriptor {
	table := map[string]ToolDescriptor{}
	register := func(d ToolDescriptor) { table[d.Name] = d }

	for _, item := range []struct {
		name, semantic string
		assistant      bool
	}{
		{"read_file", SemanticFileRead, true},
		{"workspace_files.read", SemanticFileRead, false},
		{"artifacts.read", SemanticArtifactRead, false},
		{"workspace.bootstrap", SemanticBootstrapRead, false},
	} {
		item := item
		eff := []Effect{EffectRead, EffectStat}
		register(ToolDescriptor{
			Name: item.name, Semantic: item.semantic, Assistant: item.assistant, Effects: eff,
			Prepare: func(in DescriptorInput) (PreparedPlan, error) {
				rel, err := singleReadTarget(in, item.name == "workspace.bootstrap")
				if err != nil {
					return PreparedPlan{}, err
				}
				root := defaultRootID(in.RootID)
				claims, err := dedupClaims([]ResourceClaim{{Domain: DomainFS, Root: root, Key: rel, Coverage: CoverageExact, Access: AccessShared}})
				return PreparedPlan{Tool: item.name, Semantic: item.semantic, Claims: claims, Effects: effectSet(eff...), ReadPath: rel}, err
			},
		})
	}

	register(ToolDescriptor{
		Name: "write_file", Semantic: SemanticFileWrite, Assistant: true, Modifying: true,
		Effects: []Effect{EffectRead, EffectStat, EffectWrite, EffectCreate, EffectMkdir},
		Prepare: func(in DescriptorInput) (PreparedPlan, error) {
			targets, claims, err := prepareMutatingTargets(in, 1, false)
			if err != nil {
				return PreparedPlan{}, err
			}
			return PreparedPlan{Tool: "write_file", Semantic: SemanticFileWrite, Claims: claims, Targets: targets, Modifying: true,
				Effects: effectSet(EffectRead, EffectStat, EffectWrite, EffectCreate, EffectMkdir)}, nil
		},
	})

	register(ToolDescriptor{
		Name: "edit_file", Semantic: SemanticFileEdit, Assistant: true, Modifying: true,
		Effects: []Effect{EffectRead, EffectStat, EffectWrite},
		Prepare: func(in DescriptorInput) (PreparedPlan, error) {
			targets, claims, err := prepareMutatingTargets(in, 1, false)
			if err != nil {
				return PreparedPlan{}, err
			}
			return PreparedPlan{Tool: "edit_file", Semantic: SemanticFileEdit, Claims: claims, Targets: targets, Modifying: true,
				DeferInitialGrowthRes: true, AddOldBytesToBound: true, Effects: effectSet(EffectRead, EffectStat, EffectWrite)}, nil
		},
	})

	for _, item := range []struct {
		name, semantic string
		assistant      bool
		deferGrowth    bool
		sourceBound    bool
	}{
		{"apply_file_patch", SemanticFilePatch, true, true, true},
		{"workspace_files.mutate", SemanticFileMutate, false, false, false},
		{"workspace_versions.restore", SemanticVersionRestore, false, false, false},
	} {
		item := item
		eff := []Effect{EffectRead, EffectStat, EffectWrite, EffectCreate, EffectRemove, EffectMkdir}
		register(ToolDescriptor{
			Name: item.name, Semantic: item.semantic, Assistant: item.assistant, Modifying: true, Effects: eff,
			Prepare: func(in DescriptorInput) (PreparedPlan, error) {
				targets, claims, err := prepareMutatingTargets(in, MaxTargets, true)
				if err != nil {
					return PreparedPlan{}, err
				}
				return PreparedPlan{Tool: item.name, Semantic: item.semantic, Claims: claims, Targets: targets, Modifying: true,
					DeferInitialGrowthRes: item.deferGrowth, AddSourceBytesToBound: item.sourceBound, Effects: effectSet(eff...)}, nil
			},
		})
	}

	registerDirectoryAndMemoryDescriptors(register)
	return table
}

func registerDirectoryAndMemoryDescriptors(register func(ToolDescriptor)) {
	register(ToolDescriptor{
		Name: "list_dir", Semantic: SemanticFileList, Assistant: true, Effects: []Effect{EffectStat, EffectList},
		Prepare: func(in DescriptorInput) (PreparedPlan, error) {
			rel, err := singleReadTarget(in, true)
			if err != nil {
				return PreparedPlan{}, err
			}
			claims, err := dedupClaims([]ResourceClaim{{Domain: DomainFS, Root: defaultRootID(in.RootID), Key: rel, Coverage: CoverageChildren, Access: AccessShared}})
			return PreparedPlan{Tool: "list_dir", Semantic: SemanticFileList, Claims: claims, ReadPath: rel, Effects: effectSet(EffectStat, EffectList)}, err
		},
	})

	register(ToolDescriptor{
		Name: "workspace_files.list", Semantic: SemanticFileListMeta, Effects: []Effect{EffectStat, EffectList},
		Prepare: func(in DescriptorInput) (PreparedPlan, error) {
			rel, err := singleReadTarget(in, true)
			if err != nil {
				return PreparedPlan{}, err
			}
			claims, err := dedupClaims([]ResourceClaim{{Domain: DomainFS, Root: defaultRootID(in.RootID), Key: rel, Coverage: CoverageSubtree, Access: AccessShared}})
			return PreparedPlan{Tool: "workspace_files.list", Semantic: SemanticFileListMeta, Claims: claims, ReadPath: rel, Effects: effectSet(EffectStat, EffectList)}, err
		},
	})

	for _, item := range []struct {
		name      string
		assistant bool
	}{{"search_files", true}, {"workspace_files.search", false}} {
		item := item
		eff := []Effect{EffectStat, EffectSearch, EffectRead}
		register(ToolDescriptor{
			Name: item.name, Semantic: SemanticFileSearch, Assistant: item.assistant, Effects: eff,
			Prepare: func(in DescriptorInput) (PreparedPlan, error) {
				rel, err := singleReadTarget(in, true)
				if err != nil {
					return PreparedPlan{}, err
				}
				claims, err := dedupClaims([]ResourceClaim{{Domain: DomainFS, Root: defaultRootID(in.RootID), Key: rel, Coverage: CoverageSubtree, Access: AccessShared}})
				return PreparedPlan{Tool: item.name, Semantic: SemanticFileSearch, Claims: claims, ReadPath: rel, Effects: effectSet(eff...)}, err
			},
		})
	}

	register(ToolDescriptor{
		Name: "memory_get", Semantic: SemanticMemoryGet, Assistant: true, Effects: []Effect{EffectRead, EffectStat},
		Prepare: func(in DescriptorInput) (PreparedPlan, error) {
			rel, err := singleReadTarget(in, false)
			if err != nil {
				return PreparedPlan{}, err
			}
			if !IsMemorySourcePath(rel) {
				return PreparedPlan{}, fail("undeclared_target")
			}
			claims, err := dedupClaims([]ResourceClaim{{Domain: DomainFS, Root: defaultRootID(in.RootID), Key: rel, Coverage: CoverageExact, Access: AccessShared}})
			return PreparedPlan{Tool: "memory_get", Semantic: SemanticMemoryGet, Claims: claims, ReadPath: rel, Effects: effectSet(EffectRead, EffectStat)}, err
		},
	})

	memEff := []Effect{EffectRead, EffectStat, EffectList, EffectSearch}
	register(ToolDescriptor{
		Name: "memory_search", Semantic: SemanticMemorySearch, Assistant: true, Indexing: true, Effects: memEff,
		Prepare: func(in DescriptorInput) (PreparedPlan, error) {
			if len(in.Targets) > 0 {
				return PreparedPlan{}, fail("batch_limit")
			}
			root := defaultRootID(in.RootID)
			claims, err := dedupClaims([]ResourceClaim{
				{Domain: DomainMemorySources, Root: root, Key: "sources", Coverage: CoverageExact, Access: AccessShared},
				{Domain: DomainFS, Root: root, Key: "memory.md", Coverage: CoverageExact, Access: AccessShared},
				{Domain: DomainFS, Root: root, Key: "user.md", Coverage: CoverageExact, Access: AccessShared},
				{Domain: DomainFS, Root: root, Key: "memory", Coverage: CoverageSubtree, Access: AccessShared},
				{Domain: DomainMemoryIndex, Root: root, Key: "index", Coverage: CoverageExact, Access: AccessExclusive},
			})
			return PreparedPlan{Tool: "memory_search", Semantic: SemanticMemorySearch, Claims: claims, Indexing: true, Effects: effectSet(memEff...)}, err
		},
	})

	register(ToolDescriptor{
		Name: "progress_card", Semantic: SemanticTurnProgress, Assistant: true,
		Prepare: func(in DescriptorInput) (PreparedPlan, error) {
			if len(in.Targets) > 0 || in.Path != "" {
				return PreparedPlan{}, fail("batch_limit")
			}
			claims, err := dedupClaims([]ResourceClaim{{Domain: DomainProgress, Root: defaultRootID(in.RootID), Key: "card", Coverage: CoverageExact, Access: AccessExclusive}})
			return PreparedPlan{Tool: "progress_card", Semantic: SemanticTurnProgress, Claims: claims, Effects: map[Effect]bool{}}, err
		},
	})
}
