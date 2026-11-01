package i18n

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

type guardRules struct {
	Schema           int               `json:"schema"`
	GoCheckedDirs    []string          `json:"go_checked_dirs"`
	JSCheckedDirs    []string          `json:"js_checked_dirs"`
	HTMLCheckedFiles []string          `json:"html_checked_files"`
	ExcludedPaths    []string          `json:"excluded_paths"`
	GoSymbolExempts  []symbolExemption `json:"go_symbol_exemptions"`
	JSUISinks        []jsSinkRule      `json:"js_ui_sinks"`
	GoDesktopUISinks []goSinkRule      `json:"go_desktop_ui_sinks"`
}

type jsSinkRule struct {
	Kind       string   `json:"kind"`
	Name       string   `json:"name"`
	ArgIndices []int    `json:"arg_indices"`
	Attributes []string `json:"attributes"`
}

func (r *jsSinkRule) UnmarshalJSON(data []byte) error {
	var shorthand string
	if err := json.Unmarshal(data, &shorthand); err == nil {
		if strings.TrimSpace(shorthand) == "" {
			return fmt.Errorf("empty js_ui_sinks shorthand")
		}
		*r = jsSinkRule{Kind: "call", Name: strings.TrimSpace(shorthand), ArgIndices: []int{0}}
		return nil
	}
	type rawRule jsSinkRule
	var parsed rawRule
	if err := json.Unmarshal(data, &parsed); err != nil {
		return err
	}
	if strings.TrimSpace(parsed.Name) == "" {
		return fmt.Errorf("js_ui_sinks entry missing name")
	}
	switch parsed.Kind {
	case "property", "call":
	case "attribute_call":
		if len(parsed.Attributes) == 0 {
			return fmt.Errorf("js_ui_sinks attribute_call %q missing attributes", parsed.Name)
		}
	default:
		return fmt.Errorf("unsupported js_ui_sinks kind %q", parsed.Kind)
	}
	*r = jsSinkRule(parsed)
	return nil
}

type goSinkRule struct {
	Kind       string   `json:"kind"`
	Name       string   `json:"name"`
	Receiver   string   `json:"receiver"`
	Method     string   `json:"method"`
	ProcNames  []string `json:"proc_names"`
	ArgIndices []int    `json:"arg_indices"`
}

func (r *goSinkRule) UnmarshalJSON(data []byte) error {
	var shorthand string
	if err := json.Unmarshal(data, &shorthand); err == nil {
		trimmed := strings.TrimSpace(shorthand)
		if trimmed == "" {
			return fmt.Errorf("empty go_desktop_ui_sinks shorthand")
		}
		if recv, method, ok := strings.Cut(trimmed, "."); ok {
			*r = goSinkRule{Kind: "selector", Receiver: recv, Method: method, ArgIndices: []int{0}}
			return nil
		}
		*r = goSinkRule{Kind: "func", Name: trimmed, ArgIndices: []int{0}}
		return nil
	}
	type rawRule goSinkRule
	var parsed rawRule
	if err := json.Unmarshal(data, &parsed); err != nil {
		return err
	}
	switch parsed.Kind {
	case "func":
		if strings.TrimSpace(parsed.Name) == "" {
			return fmt.Errorf("go_desktop_ui_sinks func rule missing name")
		}
	case "selector":
		if strings.TrimSpace(parsed.Method) == "" {
			return fmt.Errorf("go_desktop_ui_sinks selector rule missing method")
		}
	case "win32_proc":
		if len(parsed.ProcNames) == 0 {
			return fmt.Errorf("go_desktop_ui_sinks win32_proc rule missing proc_names")
		}
	default:
		return fmt.Errorf("unsupported go_desktop_ui_sinks kind %q", parsed.Kind)
	}
	*r = goSinkRule(parsed)
	return nil
}

type symbolExemption struct {
	File   string `json:"file"`
	Symbol string `json:"symbol"`
	Reason string `json:"reason"`
}

type jsLocaleMeta struct {
	ID                string   `json:"id"`
	Label             string   `json:"label"`
	HTMLLang          string   `json:"htmlLang"`
	Prefixes          []string `json:"prefixes"`
	ShowBrandSubtitle bool     `json:"showBrandSubtitle"`
}

var (
	guardCJKRe      = regexp.MustCompile(`[\x{3400}-\x{4dbf}\x{4e00}-\x{9fff}\x{f900}-\x{faff}\x{3000}-\x{303f}\x{ff00}-\x{ffef}]`)
	guardKeyRe      = regexp.MustCompile(`^(common|brand|web|quickinput|desktop|status|err)\.[a-z0-9_.]+$`)
	guardHTMLAttrRe = regexp.MustCompile(`data-i18n(?:-(?:placeholder|aria-label|title))?\s*=\s*["']([^"']+)["']`)
	guardJSKeyRe    = regexp.MustCompile("['\"`]((?:common|brand|web|quickinput|desktop|status|err)\\.[a-z0-9_.]+)['\"`]")
	guardEnglishRe  = regexp.MustCompile(`[A-Za-z]{2,}`)
)

func repoRootDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

func loadGuardRules(t *testing.T) guardRules {
	t.Helper()
	raw, err := os.ReadFile("guard-rules.json")
	if err != nil {
		t.Fatalf("read guard-rules.json: %v", err)
	}
	var r guardRules
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("parse guard-rules.json: %v", err)
	}
	if r.Schema != 1 {
		t.Fatalf("unexpected guard-rules.json schema: %d", r.Schema)
	}
	if len(r.JSUISinks) == 0 || len(r.GoDesktopUISinks) == 0 {
		t.Fatalf("guard-rules.json must declare non-empty js_ui_sinks and go_desktop_ui_sinks")
	}
	return r
}

func extractParams(s string) string {
	matches := paramPattern.FindAllStringSubmatch(s, -1)
	if len(matches) == 0 {
		return ""
	}
	seen := map[string]bool{}
	var names []string
	for _, m := range matches {
		if !seen[m[1]] {
			seen[m[1]] = true
			names = append(names, m[1])
		}
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

func validateCatalogBundle(m Manifest, tables map[string]map[string]string) []string {
	var errs []string
	if m.Schema != 1 || len(m.Locales) == 0 {
		return []string{"invalid manifest schema or empty locales"}
	}
	seen := map[string]bool{}
	for _, loc := range m.Locales {
		if loc.ID == "" || loc.Label == "" || loc.HTMLLang == "" || len(loc.Prefixes) == 0 {
			errs = append(errs, fmt.Sprintf("incomplete locale metadata: %+v", loc))
		}
		if seen[loc.ID] {
			errs = append(errs, "duplicate locale ID: "+loc.ID)
		}
		seen[loc.ID] = true
	}
	if !seen[m.DefaultFallback] {
		errs = append(errs, "default_fallback not registered: "+m.DefaultFallback)
	}
	baseID := m.Locales[0].ID
	base := tables[baseID]
	if len(base) == 0 {
		return append(errs, "empty baseline catalog: "+baseID)
	}
	for _, loc := range m.Locales {
		cat := tables[loc.ID]
		if cat == nil {
			errs = append(errs, "missing catalog: "+loc.ID)
			continue
		}
		for k, baseVal := range base {
			val, ok := cat[k]
			if !ok {
				errs = append(errs, fmt.Sprintf("locale %s missing key %s", loc.ID, k))
				continue
			}
			if val == "" {
				errs = append(errs, fmt.Sprintf("locale %s empty value for %s", loc.ID, k))
			}
			if loc.DisallowCJK && guardCJKRe.MatchString(val) {
				errs = append(errs, fmt.Sprintf("locale %s (disallow_cjk) has CJK in %s: %q", loc.ID, k, val))
			}
			if p1, p2 := extractParams(baseVal), extractParams(val); p1 != p2 {
				errs = append(errs, fmt.Sprintf("locale %s placeholder mismatch in %s: {%s} vs {%s}", loc.ID, k, p1, p2))
			}
		}
		for k := range cat {
			if _, ok := base[k]; !ok {
				errs = append(errs, fmt.Sprintf("locale %s extra key %s", loc.ID, k))
			}
		}
	}
	return errs
}

func marshalJSJSON(v any, indent bool) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if indent {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

func renderExpectedJSModule(m Manifest, tables map[string]map[string]string, prefixes []string) (string, error) {
	meta := make([]jsLocaleMeta, 0, len(m.Locales))
	for _, loc := range m.Locales {
		meta = append(meta, jsLocaleMeta{
			ID:                loc.ID,
			Label:             loc.Label,
			HTMLLang:          loc.HTMLLang,
			Prefixes:          loc.Prefixes,
			ShowBrandSubtitle: loc.ShowBrandSubtitle,
		})
	}
	var catBuf bytes.Buffer
	catBuf.WriteString("{\n")
	for idx, loc := range m.Locales {
		src := tables[loc.ID]
		var keys []string
		for k := range src {
			for _, p := range prefixes {
				if strings.HasPrefix(k, p) {
					keys = append(keys, k)
					break
				}
			}
		}
		sort.Strings(keys)
		idJSON, _ := marshalJSJSON(loc.ID, false)
		catBuf.WriteString("  " + idJSON + ": {\n")
		for ki, k := range keys {
			kJSON, _ := marshalJSJSON(k, false)
			vJSON, _ := marshalJSJSON(src[k], false)
			comma := ","
			if ki == len(keys)-1 {
				comma = ""
			}
			catBuf.WriteString("    " + kJSON + ": " + vJSON + comma + "\n")
		}
		endComma := ","
		if idx == len(m.Locales)-1 {
			endComma = ""
		}
		catBuf.WriteString("  }" + endComma + "\n")
	}
	catBuf.WriteString("}")
	fbJSON, err := marshalJSJSON(m.DefaultFallback, false)
	if err != nil {
		return "", err
	}
	metaJSON, err := marshalJSJSON(meta, true)
	if err != nil {
		return "", err
	}
	return "// Code generated by scripts/sync-i18n.mjs; DO NOT EDIT.\n" +
		"export const defaultFallback = " + fbJSON + ";\n" +
		"export const locales = Object.freeze(" + metaJSON + ");\n" +
		"export const catalog = Object.freeze(" + catBuf.String() + ");\n", nil
}

func isExcludedRel(rel string, excluded []string) bool {
	for _, ex := range excluded {
		if rel == ex || strings.HasPrefix(rel, ex) {
			return true
		}
	}
	return false
}

func exprContainsNaturalLiteral(expr ast.Expr) (string, bool) {
	var hit string
	var visit func(ast.Node) bool
	visit = func(n ast.Node) bool {
		if hit != "" || n == nil {
			return false
		}
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "i18n" && (sel.Sel.Name == "T" || sel.Sel.Name == "LocalizeMessage") {
					for i := 2; i < len(call.Args); i++ {
						if comp, ok := call.Args[i].(*ast.CompositeLit); ok {
							for _, elt := range comp.Elts {
								if kv, ok := elt.(*ast.KeyValueExpr); ok {
									ast.Inspect(kv.Value, visit)
								} else {
									ast.Inspect(elt, visit)
								}
							}
						} else {
							ast.Inspect(call.Args[i], visit)
						}
					}
					return false
				}
			}
		}
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		val, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		if guardKeyRe.MatchString(val) {
			return true
		}
		if guardCJKRe.MatchString(val) || guardEnglishRe.MatchString(val) {
			hit = val
			return false
		}
		return true
	}
	ast.Inspect(expr, visit)
	return hit, hit != ""
}

func checkSinkArgs(fset *token.FileSet, rel, label string, call *ast.CallExpr, indices []int) []string {
	var errs []string
	if len(indices) == 0 {
		for _, arg := range call.Args {
			if lit, bad := exprContainsNaturalLiteral(arg); bad {
				pos := fset.Position(call.Pos())
				errs = append(errs, fmt.Sprintf("%s:%d: direct UI literal %q in %s", rel, pos.Line, lit, label))
			}
		}
		return errs
	}
	for _, idx := range indices {
		if idx >= 0 && idx < len(call.Args) {
			if lit, bad := exprContainsNaturalLiteral(call.Args[idx]); bad {
				pos := fset.Position(call.Pos())
				errs = append(errs, fmt.Sprintf("%s:%d: direct UI literal %q in %s", rel, pos.Line, lit, label))
			}
		}
	}
	return errs
}

func checkDesktopSinkCall(fset *token.FileSet, rel string, call *ast.CallExpr, sinks []goSinkRule) []string {
	var errs []string
	for _, sink := range sinks {
		switch sink.Kind {
		case "func":
			if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == sink.Name {
				errs = append(errs, checkSinkArgs(fset, rel, sink.Name, call, sink.ArgIndices)...)
			}
		case "selector":
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != sink.Method {
				continue
			}
			if sink.Receiver != "" {
				ident, ok := sel.X.(*ast.Ident)
				if !ok || ident.Name != sink.Receiver {
					continue
				}
			}
			label := sink.Method
			if sink.Receiver != "" {
				label = sink.Receiver + "." + sink.Method
			}
			errs = append(errs, checkSinkArgs(fset, rel, label, call, sink.ArgIndices)...)
		case "win32_proc":
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Call" {
				continue
			}
			inner, ok := sel.X.(*ast.CallExpr)
			if !ok || len(inner.Args) != 1 {
				continue
			}
			procSel, ok := inner.Fun.(*ast.SelectorExpr)
			if !ok || procSel.Sel.Name != "NewProc" {
				continue
			}
			bl, ok := inner.Args[0].(*ast.BasicLit)
			if !ok || bl.Kind != token.STRING {
				continue
			}
			procName, err := strconv.Unquote(bl.Value)
			if err != nil {
				continue
			}
			for _, candidate := range sink.ProcNames {
				if procName == candidate {
					errs = append(errs, checkSinkArgs(fset, rel, procName, call, sink.ArgIndices)...)
					break
				}
			}
		}
	}
	return errs
}

func checkGoFileAST(rel string, src []byte, exemptSymbols map[string]bool, validKeys map[string]bool, sinks []goSinkRule) (errs []string, refs map[string]bool) {
	refs = map[string]bool{}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, rel, src, 0)
	if err != nil {
		return []string{fmt.Sprintf("%s: parse error: %v", rel, err)}, refs
	}
	checkSubtree := func(node ast.Node, exempted bool) {
		ast.Inspect(node, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.ImportSpec:
				return false
			case *ast.CallExpr:
				errs = append(errs, checkDesktopSinkCall(fset, rel, x, sinks)...)
				checkI18nCallKey(fset, rel, x, validKeys, &errs, refs)
			case *ast.BasicLit:
				if x.Kind != token.STRING {
					return true
				}
				val, err := strconv.Unquote(x.Value)
				if err != nil {
					return true
				}
				if guardKeyRe.MatchString(val) {
					refs[val] = true
					if !validKeys[val] {
						pos := fset.Position(x.Pos())
						errs = append(errs, fmt.Sprintf("%s:%d: unknown i18n key literal %q", rel, pos.Line, val))
					}
				}
				if !exempted && guardCJKRe.MatchString(val) {
					pos := fset.Position(x.Pos())
					errs = append(errs, fmt.Sprintf("%s:%d: unexempted CJK string literal %q", rel, pos.Line, val))
				}
			}
			return true
		})
	}
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			if d.Tok == token.IMPORT {
				continue
			}
			for _, spec := range d.Specs {
				if vs, ok := spec.(*ast.ValueSpec); ok {
					exempt := false
					for _, name := range vs.Names {
						if exemptSymbols[name.Name] {
							exempt = true
						}
					}
					checkSubtree(vs, exempt)
				} else {
					checkSubtree(spec, false)
				}
			}
		case *ast.FuncDecl:
			checkSubtree(d, exemptSymbols[d.Name.Name])
		default:
			checkSubtree(d, false)
		}
	}
	return errs, refs
}

func checkI18nCallKey(fset *token.FileSet, rel string, call *ast.CallExpr, validKeys map[string]bool, errs *[]string, refs map[string]bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "i18n" {
		return
	}
	keyArgIdx := -1
	switch sel.Sel.Name {
	case "Err", "ErrArgs", "Wrap", "FromKey", "HasKey":
		keyArgIdx = 0
	case "T", "LocalizeMessage":
		keyArgIdx = 1
	}
	if keyArgIdx < 0 || keyArgIdx >= len(call.Args) {
		return
	}
	if bl, ok := call.Args[keyArgIdx].(*ast.BasicLit); ok && bl.Kind == token.STRING {
		val, err := strconv.Unquote(bl.Value)
		if err == nil && val != "" {
			refs[val] = true
			if !validKeys[val] {
				pos := fset.Position(bl.Pos())
				*errs = append(*errs, fmt.Sprintf("%s:%d: unknown key %q in i18n.%s", rel, pos.Line, val, sel.Sel.Name))
			}
		}
	}
}

func stripJSCommentsForKeyScan(src string) string {
	var out strings.Builder
	for i := 0; i < len(src); {
		ch := src[i]
		if ch == '/' && i+1 < len(src) && src[i+1] == '/' {
			for i < len(src) && src[i] != '\n' {
				i++
			}
			continue
		}
		if ch == '/' && i+1 < len(src) && src[i+1] == '*' {
			i += 2
			for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
				if src[i] == '\n' {
					out.WriteByte('\n')
				}
				i++
			}
			i += 2
			out.WriteByte(' ')
			continue
		}
		if ch == '\'' || ch == '"' || ch == '`' {
			quote := ch
			out.WriteByte(ch)
			i++
			for i < len(src) && src[i] != quote {
				if src[i] == '\\' && i+1 < len(src) {
					out.WriteByte(src[i])
					out.WriteByte(src[i+1])
					i += 2
					continue
				}
				out.WriteByte(src[i])
				i++
			}
			if i < len(src) {
				out.WriteByte(src[i])
				i++
			}
			continue
		}
		out.WriteByte(ch)
		i++
	}
	return out.String()
}

func scanJSKeyLiterals(rel, src string, allowedKeys, referenced map[string]bool) []string {
	var errs []string
	stripped := stripJSCommentsForKeyScan(src)
	for _, m := range guardJSKeyRe.FindAllStringSubmatch(stripped, -1) {
		referenced[m[1]] = true
		if !allowedKeys[m[1]] {
			errs = append(errs, fmt.Sprintf("%s: unknown i18n key literal %q", rel, m[1]))
		}
	}
	return errs
}

func filterKeysByPrefixes(all map[string]bool, prefixes []string) map[string]bool {
	out := map[string]bool{}
	for k := range all {
		for _, p := range prefixes {
			if strings.HasPrefix(k, p) {
				out[k] = true
				break
			}
		}
	}
	return out
}

func walkCheckedJSRefs(t *testing.T, root string, rules guardRules, allKeys, webKeys, quickKeys, referenced map[string]bool) []string {
	t.Helper()
	var errs []string
	for _, jsDir := range rules.JSCheckedDirs {
		dirPath := filepath.Join(root, filepath.FromSlash(jsDir))
		err := filepath.WalkDir(dirPath, func(p string, d os.DirEntry, walkErr error) error {
			if walkErr != nil || d.IsDir() || (!strings.HasSuffix(d.Name(), ".js") && !strings.HasSuffix(d.Name(), ".mjs")) {
				return walkErr
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if isExcludedRel(rel, rules.ExcludedPaths) {
				return nil
			}
			raw, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			allowed := allKeys
			if strings.HasPrefix(rel, "web/") {
				allowed = webKeys
			} else if strings.HasPrefix(rel, "internal/quickinput/") {
				allowed = quickKeys
			}
			errs = append(errs, scanJSKeyLiterals(rel, string(raw), allowed, referenced)...)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", jsDir, err)
		}
	}
	return errs
}

func TestG1CatalogSymmetryAndGeneratedModules(t *testing.T) {
	loadBundle()
	if errs := validateCatalogBundle(manifestData, catalogData); len(errs) > 0 {
		t.Fatalf("G1 catalog validation failed:\n%s", strings.Join(errs, "\n"))
	}
	root := repoRootDir(t)
	targets := []struct {
		rel      string
		prefixes []string
	}{
		{"web/i18n-catalog.mjs", manifestData.WebPrefixes},
		{"internal/quickinput/assets/i18n-catalog.mjs", manifestData.QuickInputPrefixes},
	}
	for _, target := range targets {
		expected, err := renderExpectedJSModule(manifestData, catalogData, target.prefixes)
		if err != nil {
			t.Fatal(err)
		}
		actual, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(target.rel)))
		if err != nil {
			t.Fatalf("read %s: %v", target.rel, err)
		}
		if string(actual) != expected {
			t.Fatalf("%s is out of sync with internal/i18n/locales/*.json (run node ./scripts/sync-i18n.mjs)", target.rel)
		}
	}
}

func TestG1KanjiLocaleExtensionAllowedWhenDisallowCJKUnset(t *testing.T) {
	m := Manifest{
		Schema:          1,
		DefaultFallback: "en-US",
		Locales: []LocaleEntry{
			{ID: "en-US", Label: "English", HTMLLang: "en-US", Prefixes: []string{"en"}, DisallowCJK: true},
			{ID: "ja-JP", Label: "日本語", HTMLLang: "ja-JP", Prefixes: []string{"ja"}, DisallowCJK: false},
		},
	}
	tables := map[string]map[string]string{
		"en-US": {"web.topbar.settings": "Settings ({count})"},
		"ja-JP": {"web.topbar.settings": "設定 ({count})"},
	}
	if errs := validateCatalogBundle(m, tables); len(errs) > 0 {
		t.Fatalf("expected ja-JP Kanji catalog to pass G1, got: %v", errs)
	}
	if got, ok := MatchLocaleAgainst(m, "ja_JP.UTF-8"); !ok || got != "ja-JP" {
		t.Fatalf("expected ja-JP match without code edits, got %q %v", got, ok)
	}
	tables["en-US"]["web.topbar.settings"] = "Settings 设置 ({count})"
	if errs := validateCatalogBundle(m, tables); len(errs) == 0 {
		t.Fatal("expected disallow_cjk locale to reject residual CJK")
	}
}

func TestG2AndG4FullStackGuardrails(t *testing.T) {
	loadBundle()
	rules := loadGuardRules(t)
	root := repoRootDir(t)
	validKeys := map[string]bool{}
	for k := range catalogData[manifestData.DefaultFallback] {
		validKeys[k] = true
	}
	webKeys := filterKeysByPrefixes(validKeys, manifestData.WebPrefixes)
	quickKeys := filterKeysByPrefixes(validKeys, manifestData.QuickInputPrefixes)
	exemptByFile := map[string]map[string]bool{}
	for _, ex := range rules.GoSymbolExempts {
		if ex.Reason == "" {
			t.Fatalf("go_symbol_exemptions entry %s:%s missing reason", ex.File, ex.Symbol)
		}
		if exemptByFile[ex.File] == nil {
			exemptByFile[ex.File] = map[string]bool{}
		}
		exemptByFile[ex.File][ex.Symbol] = true
	}

	referenced := map[string]bool{}
	var allErrs []string

	for _, dirRel := range rules.GoCheckedDirs {
		dirPath := filepath.Join(root, filepath.FromSlash(dirRel))
		err := filepath.WalkDir(dirPath, func(p string, d os.DirEntry, walkErr error) error {
			if walkErr != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
				return walkErr
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if isExcludedRel(rel, rules.ExcludedPaths) {
				return nil
			}
			src, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			errs, refs := checkGoFileAST(rel, src, exemptByFile[rel], validKeys, rules.GoDesktopUISinks)
			allErrs = append(allErrs, errs...)
			for k := range refs {
				referenced[k] = true
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dirRel, err)
		}
	}

	for _, htmlRel := range rules.HTMLCheckedFiles {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(htmlRel)))
		if err != nil {
			t.Fatalf("read %s: %v", htmlRel, err)
		}
		allowed := quickKeys
		if strings.HasPrefix(htmlRel, "web/") {
			allowed = webKeys
		}
		for _, m := range guardHTMLAttrRe.FindAllStringSubmatch(string(raw), -1) {
			referenced[m[1]] = true
			if !allowed[m[1]] {
				allErrs = append(allErrs, fmt.Sprintf("%s: unknown i18n key %q", htmlRel, m[1]))
			}
		}
	}

	allErrs = append(allErrs, walkCheckedJSRefs(t, root, rules, validKeys, webKeys, quickKeys, referenced)...)

	var deadKeys []string
	for k := range validKeys {
		if !referenced[k] {
			deadKeys = append(deadKeys, k)
		}
	}
	sort.Strings(deadKeys)
	for _, k := range deadKeys {
		allErrs = append(allErrs, "unused dead catalog key: "+k)
	}
	if len(allErrs) > 0 {
		t.Fatalf("i18n static guardrails failed (%d issues):\n%s", len(allErrs), strings.Join(allErrs, "\n"))
	}
}

func TestG4GoSyntheticViolationsDetected(t *testing.T) {
	rules := loadGuardRules(t)
	valid := map[string]bool{"desktop.tray.open_web": true}
	src := []byte(`package sample
func bad() {
	_ = "硬编码中文"
	menuItem(0, 1, 0, "Open Asterhand")
	messageBox.Call(0, ptr("Fatal startup error"), ptr("Asterhand"), 0)
	uiDLL.NewProc("SetWindowTextW").Call(0, ptr("Quick Input"))
}
`)
	errs, _ := checkGoFileAST("internal/desktop/sample.go", src, nil, valid, rules.GoDesktopUISinks)
	if len(errs) < 4 {
		t.Fatalf("expected CJK + 3 desktop UI sink violations, got (%d): %v", len(errs), errs)
	}

	// Registering a new sink rule in go_desktop_ui_sinks immediately expands AST detection without code changes.
	extendedSinks := append([]goSinkRule{}, rules.GoDesktopUISinks...)
	extendedSinks = append(extendedSinks,
		goSinkRule{Kind: "func", Name: "customBanner", ArgIndices: []int{0}},
		goSinkRule{Kind: "selector", Receiver: "toast", Method: "Show", ArgIndices: []int{0}},
	)
	customSrc := []byte(`package sample
func extra() {
	customBanner("Save changes")
	toast.Show("Connection failed")
}
`)
	beforeErrs, _ := checkGoFileAST("internal/desktop/extra.go", customSrc, nil, valid, rules.GoDesktopUISinks)
	if len(beforeErrs) != 0 {
		t.Fatalf("expected unregistered sinks not to match before registration, got: %v", beforeErrs)
	}
	afterErrs, _ := checkGoFileAST("internal/desktop/extra.go", customSrc, nil, valid, extendedSinks)
	if len(afterErrs) != 2 {
		t.Fatalf("expected 2 violations after registering new go_desktop_ui_sinks, got (%d): %v", len(afterErrs), afterErrs)
	}

	// Unknown JS map key literals are also rejected by the Go full-stack scanner.
	refs := map[string]bool{}
	jsErrs := scanJSKeyLiterals("web/sample.mjs", "// 'web.comment.ignored'\nconst keys = {done: 'web.not_a_real_key'};\nt(keys.done);\n", valid, refs)
	if len(jsErrs) != 1 || !strings.Contains(jsErrs[0], "web.not_a_real_key") {
		t.Fatalf("expected unknown JS map key literal error, got: %v", jsErrs)
	}
}
