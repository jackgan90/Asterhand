package workspace

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Domain string

const (
	DomainFS            Domain = "fs"
	DomainMemorySources Domain = "memory_sources"
	DomainMemoryIndex   Domain = "memory_index"
	DomainProgress      Domain = "progress"
)

type Coverage string

const (
	CoverageExact    Coverage = "exact"
	CoverageChildren Coverage = "children"
	CoverageSubtree  Coverage = "subtree"
)

type AccessMode string

const (
	AccessShared    AccessMode = "S"
	AccessExclusive AccessMode = "X"
)

type Effect string

const (
	EffectRead   Effect = "read"
	EffectStat   Effect = "stat"
	EffectList   Effect = "list"
	EffectSearch Effect = "search"
	EffectWrite  Effect = "write"
	EffectCreate Effect = "create"
	EffectRemove Effect = "remove"
	EffectMkdir  Effect = "mkdir"
)

const (
	SemanticFileRead       = "file.read"
	SemanticArtifactRead   = "artifact.read"
	SemanticBootstrapRead  = "system.bootstrap_read"
	SemanticFileWrite      = "file.write"
	SemanticFileEdit       = "file.edit"
	SemanticFilePatch      = "file.patch"
	SemanticFileMutate     = "file.mutate"
	SemanticVersionRestore = "version.restore"
	SemanticFileList       = "file.list"
	SemanticFileListMeta   = "file.list_meta"
	SemanticFileSearch     = "file.search"
	SemanticMemoryGet      = "memory.get"
	SemanticMemorySearch   = "memory.search"
	SemanticTurnProgress   = "turn.progress"
)

type ResourceClaim struct {
	Domain   Domain     `json:"domain"`
	Root     string     `json:"root"`
	Key      string     `json:"key"`
	Coverage Coverage   `json:"coverage"`
	Access   AccessMode `json:"access"`
}

type PreparedPlan struct {
	Tool                  string
	Semantic              string
	Claims                []ResourceClaim
	Effects               map[Effect]bool
	Targets               []Target
	ReadPath              string
	Summary               string
	Modifying             bool
	Indexing              bool
	DeferInitialGrowthRes bool
	AddSourceBytesToBound bool
	AddOldBytesToBound    bool
}

func sanitizeSummary(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if !utf8.ValidString(raw) {
		return "", fail("invalid_request")
	}
	var b strings.Builder
	b.Grow(min(len(raw), MaxSummaryBytes))
	pendingSpace := false
	started := false
	for _, r := range raw {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			if started {
				pendingSpace = true
			}
			continue
		}
		if pendingSpace {
			b.WriteByte(' ')
			pendingSpace = false
		}
		b.WriteRune(r)
		started = true
	}
	cleaned := b.String()
	if cleaned == "" {
		return "", nil
	}
	if utf8.RuneCountInString(cleaned) <= MaxSummaryRunes && len(cleaned) <= MaxSummaryBytes {
		return cleaned, nil
	}
	const suffix = "…"
	var out strings.Builder
	out.Grow(MaxSummaryBytes)
	runes := 0
	for _, r := range cleaned {
		rl := utf8.RuneLen(r)
		if runes+2 > MaxSummaryRunes || out.Len()+rl+len(suffix) > MaxSummaryBytes {
			out.WriteString(suffix)
			break
		}
		out.WriteRune(r)
		runes++
	}
	return out.String(), nil
}

func summarizeTargets(targets []Target) string {
	if len(targets) == 0 {
		return ""
	}
	limit := min(len(targets), 3)
	parts := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		if targets[i].Path != "" {
			parts = append(parts, targets[i].Path)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	joined := strings.Join(parts, ", ")
	if len(targets) > 3 {
		joined = fmt.Sprintf("%s (+%d)", joined, len(targets)-3)
	}
	out, err := sanitizeSummary(joined)
	if err != nil {
		return ""
	}
	return out
}

type DescriptorInput struct {
	WorkspaceRoot string
	RootID        string
	Targets       []Target
	Path          string
	NewBytes      int64
}

type ToolDescriptor struct {
	Name      string
	Semantic  string
	Assistant bool
	Modifying bool
	Indexing  bool
	Effects   []Effect
	Prepare   func(in DescriptorInput) (PreparedPlan, error)
}

func AssistantToolNames() []string {
	return []string{
		"read_file",
		"write_file",
		"edit_file",
		"apply_file_patch",
		"list_dir",
		"search_files",
		"memory_search",
		"memory_get",
		"progress_card",
	}
}

func IsMemorySourcePath(rel string) bool {
	k := pathKey(rel)
	return k == "memory.md" || k == "user.md" || k == "memory" || strings.HasPrefix(k, "memory/")
}

func ValidClaim(c ResourceClaim) bool {
	switch c.Domain {
	case DomainFS, DomainMemorySources, DomainMemoryIndex, DomainProgress:
	default:
		return false
	}
	if c.Root == "" {
		return false
	}
	switch c.Coverage {
	case CoverageExact:
	case CoverageChildren, CoverageSubtree:
		if c.Domain != DomainFS {
			return false
		}
	default:
		return false
	}
	return c.Access == AccessShared || c.Access == AccessExclusive
}

func parentKey(k string) (string, bool) {
	if k == "" {
		return "", false
	}
	idx := strings.LastIndexByte(k, '/')
	if idx < 0 {
		return "", true
	}
	return k[:idx], true
}

func isDescendantKey(child, ancestor string) bool {
	if child == "" || child == ancestor {
		return false
	}
	if ancestor == "" {
		return true
	}
	return strings.HasPrefix(child, ancestor+"/")
}

func CoverageCoversPath(dirOrPath string, cov Coverage, target string) bool {
	d := pathKey(dirOrPath)
	t := pathKey(target)
	switch cov {
	case CoverageExact:
		return d == t
	case CoverageChildren:
		if d == t {
			return true
		}
		p, ok := parentKey(t)
		return ok && p == d
	case CoverageSubtree:
		return d == t || isDescendantKey(t, d)
	default:
		return false
	}
}

func CoverageIntersects(aKey string, aCov Coverage, bKey string, bCov Coverage) bool {
	a := pathKey(aKey)
	b := pathKey(bKey)
	if aCov == CoverageExact {
		return CoverageCoversPath(b, bCov, a)
	}
	if bCov == CoverageExact {
		return CoverageCoversPath(a, aCov, b)
	}
	if aCov == CoverageChildren && bCov == CoverageChildren {
		if a == b {
			return true
		}
		pa, oka := parentKey(a)
		pb, okb := parentKey(b)
		return (oka && pa == b) || (okb && pb == a)
	}
	if aCov == CoverageChildren && bCov == CoverageSubtree {
		if a == b || isDescendantKey(a, b) {
			return true
		}
		pb, okb := parentKey(b)
		return okb && pb == a
	}
	if aCov == CoverageSubtree && bCov == CoverageChildren {
		return CoverageIntersects(b, bCov, a, aCov)
	}
	// Both subtree: intersect iff one equals or is a descendant of the other.
	return a == b || isDescendantKey(a, b) || isDescendantKey(b, a)
}

func ClaimsConflict(a, b ResourceClaim) bool {
	if a.Domain != b.Domain || a.Root != b.Root {
		return false
	}
	if a.Access == AccessShared && b.Access == AccessShared {
		return false
	}
	return CoverageIntersects(a.Key, a.Coverage, b.Key, b.Coverage)
}

func ClaimCoversAccess(claims []ResourceClaim, effects map[Effect]bool, root, rel string, eff Effect, needWrite bool) bool {
	if !effects[eff] {
		return false
	}
	k := pathKey(rel)
	for _, c := range claims {
		if c.Domain != DomainFS || c.Root != root {
			continue
		}
		if needWrite && c.Access != AccessExclusive {
			continue
		}
		if CoverageCoversPath(c.Key, c.Coverage, k) {
			return true
		}
	}
	return false
}

func missingAncestorKeys(workspaceRoot, rel string) []string {
	if workspaceRoot == "" {
		return nil
	}
	dir := path.Dir(rel)
	if dir == "." || dir == "" {
		return nil
	}
	parts := strings.Split(dir, "/")
	var missing []string
	cur := ""
	for _, p := range parts {
		if cur == "" {
			cur = p
		} else {
			cur = cur + "/" + p
		}
		full := filepath.Join(workspaceRoot, filepath.FromSlash(cur))
		if _, err := os.Lstat(full); os.IsNotExist(err) {
			missing = append(missing, pathKey(cur))
		}
	}
	return missing
}

func effectSet(items ...Effect) map[Effect]bool {
	m := make(map[Effect]bool, len(items))
	for _, item := range items {
		m[item] = true
	}
	return m
}

func dedupClaims(claims []ResourceClaim) ([]ResourceClaim, error) {
	type key struct {
		d Domain
		r string
		k string
		c Coverage
	}
	merged := map[key]ResourceClaim{}
	var order []key
	for _, c := range claims {
		c.Key = pathKey(c.Key)
		if !ValidClaim(c) {
			return nil, fail("invalid_descriptor")
		}
		id := key{c.Domain, c.Root, c.Key, c.Coverage}
		if prev, ok := merged[id]; ok {
			if c.Access == AccessExclusive {
				prev.Access = AccessExclusive
				merged[id] = prev
			}
			continue
		}
		merged[id] = c
		order = append(order, id)
	}
	out := make([]ResourceClaim, 0, len(order))
	for _, id := range order {
		out = append(out, merged[id])
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Domain != out[j].Domain {
			return out[i].Domain < out[j].Domain
		}
		if out[i].Root != out[j].Root {
			return out[i].Root < out[j].Root
		}
		if out[i].Key != out[j].Key {
			return out[i].Key < out[j].Key
		}
		return out[i].Coverage < out[j].Coverage
	})
	return out, nil
}
