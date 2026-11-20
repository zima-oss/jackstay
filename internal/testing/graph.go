package scopes

import (
	"fmt"
	"slices"
	"strings"
)

// The task graph (docs/design/land.md section 7): a scope or step names in `deps` (or, read as
// deps, `steps`) what must pass before it. A run walks the graph from the scopes asked for, each
// name once, deps first, in declared order; `systems` says where a node can run at all and `ci`
// where CI runs a scope.

// operatingSystems are the systems `ci` and `systems` may name, as Go's runtime names them.
var operatingSystems = []string{"darwin", "linux"}

// depsOf are a node's deps: its `deps`, or its `steps` read as deps.
func depsOf(entry map[string]any) []string {
	if has(entry, "deps") {
		return textsOf(entry["deps"])
	}
	return textsOf(entry["steps"])
}

// isComposite says whether a node runs nothing of its own: a scope or step with no `run`.
func isComposite(entry map[string]any) bool { return !has(entry, "run") }

// runsOn says whether a node's own `systems` (absent: any) include the system.
func runsOn(entry map[string]any, system string) bool {
	return !has(entry, "systems") || slices.Contains(textsOf(entry["systems"]), system)
}

// needsTestArguments says whether a node's words need names or IDs from the command line
// ({tests}, {test_args} or a typed selector), which a dep is never given.
func needsTestArguments(entry map[string]any) bool {
	used := placeholdersOf(textsOf(entry["run"]))
	return has(entry, "selector") || slices.Contains(used, "tests") || slices.Contains(used, "test_args")
}

// graph is the nodes of a `test` section seen from one system.
type graph struct {
	section map[string]any
	system  string
	// settled is each node's eligibility on the system, once known.
	settled map[string]bool
}

func newGraph(section map[string]any, system string) *graph {
	return &graph{section: section, system: system, settled: map[string]bool{}}
}

// eligible says whether a node can run on the graph's system (land.md section 7.2): a node with
// `run` when its `systems` include the system and every dep is eligible; a composite when its
// `systems` include the system and at least one dep is eligible. An unknown name is not.
func (nodes *graph) eligible(name string) bool {
	if value, known := nodes.settled[name]; known {
		return value
	}
	// A cycle (refused by `jst check`) reads as not eligible rather than recursing for ever.
	nodes.settled[name] = false
	_, entry := find(nodes.section, name)
	value := entry != nil && runsOn(entry, nodes.system)
	if value {
		composite := isComposite(entry)
		value = !composite
		for _, dep := range depsOf(entry) {
			if composite && nodes.eligible(dep) {
				value = true
				break
			}
			if !composite && !nodes.eligible(dep) {
				value = false
				break
			}
		}
	}
	nodes.settled[name] = value
	return value
}

// breaker is the node that makes a name not eligible: the first dep on the path that is not
// eligible, followed down to a node whose own `systems` leave the system out.
func (nodes *graph) breaker(name string) string {
	_, entry := find(nodes.section, name)
	if entry == nil || !runsOn(entry, nodes.system) {
		return name
	}
	for _, dep := range depsOf(entry) {
		if !nodes.eligible(dep) {
			return nodes.breaker(dep)
		}
	}
	return name
}

// walk is the run's order from the scopes asked for: depth first, in the order given, each node
// after its deps in their declared order, each name once. A node that is not eligible is listed
// and its deps are not visited through it.
func (nodes *graph) walk(asked []string) []string {
	var order []string
	listed := map[string]bool{}
	var visit func(name string)
	visit = func(name string) {
		if listed[name] {
			return
		}
		listed[name] = true
		if nodes.eligible(name) {
			_, entry := find(nodes.section, name)
			for _, dep := range depsOf(entry) {
				visit(dep)
			}
		}
		order = append(order, name)
	}
	for _, name := range asked {
		visit(name)
	}
	return order
}

// RunOrder is the order in which the nodes with `run` of a scope's graph run on the system (the
// walk of docs/design/land.md section 7.2, without the nodes that cannot run there).
func RunOrder(section map[string]any, name, system string) []string {
	nodes := newGraph(section, system)
	var order []string
	for _, each := range nodes.walk([]string{name}) {
		if _, entry := find(section, each); entry != nil && !isComposite(entry) && nodes.eligible(each) {
			order = append(order, each)
		}
	}
	return order
}

// ciScopes are the scopes whose own `ci` names the system, in the order `jst test list` shows.
func ciScopes(section map[string]any, system string) []string {
	scopes := objectOf(section["scopes"])
	var found []string
	for _, name := range orderedScopes(scopes) {
		if slices.Contains(textsOf(objectOf(scopes[name])["ci"]), system) {
			found = append(found, name)
		}
	}
	return found
}

// coveredOn are the nodes CI covers on the system: those its walk from the scopes with that `ci`
// reaches and that are eligible there.
func coveredOn(section map[string]any, system string) map[string]bool {
	nodes := newGraph(section, system)
	covered := map[string]bool{}
	for _, name := range nodes.walk(ciScopes(section, system)) {
		if nodes.eligible(name) {
			covered[name] = true
		}
	}
	return covered
}

// declaresWhere says whether a node of the section says where it runs (`ci` or `systems`).
func declaresWhere(section map[string]any) bool {
	for _, kind := range []string{"scopes", "steps"} {
		entries := objectOf(section[kind])
		for _, name := range sortedKeys(entries) {
			if has(objectOf(entries[name]), "ci") || has(objectOf(entries[name]), "systems") {
				return true
			}
		}
	}
	return false
}

// orderedScopes are the scopes' names: the six in their order, then the extras by name.
func orderedScopes(scopes map[string]any) []string {
	var order []string
	for _, name := range six {
		if has(scopes, name) {
			order = append(order, name)
		}
	}
	for _, name := range sortedKeys(scopes) {
		if !slices.Contains(six, name) {
			order = append(order, name)
		}
	}
	return order
}

// graphProblems are what is wrong with the graph of a `test` section: `steps` with `deps`, an
// unknown dep, a dep that needs test arguments, a cycle, and `ci` naming a system where the scope
// cannot run.
func graphProblems(section map[string]any) []string {
	var found []string
	known := true
	for _, kind := range []string{"scopes", "steps"} {
		entries := objectOf(section[kind])
		for _, name := range sortedKeys(entries) {
			entry := objectOf(entries[name])
			here := fmt.Sprintf("test.%s.%s", kind, name)
			if has(entry, "steps") && has(entry, "deps") {
				found = append(found, fmt.Sprintf("`%s` has both `steps` and `deps`: `steps` is read as `deps`; keep one", here))
			}
			for _, dep := range depsOf(entry) {
				_, depEntry := find(section, dep)
				switch {
				case depEntry == nil:
					known = false
					found = append(found, fmt.Sprintf("`%s` names '%s', which is neither a step nor a scope", here, dep))
				case !isComposite(depEntry) && needsTestArguments(depEntry):
					found = append(found, fmt.Sprintf("`%s` depends on '%s', whose words need test names ({tests}, {test_args} or a selector): nothing gives a dep names", here, dep))
				}
			}
		}
	}
	if !known {
		return found
	}
	if cycle := cycleOf(section); cycle != "" {
		return append(found, "`test`: the deps go round in a circle: "+cycle)
	}
	scopes := objectOf(section["scopes"])
	for _, name := range sortedKeys(scopes) {
		for _, system := range textsOf(objectOf(scopes[name])["ci"]) {
			nodes := newGraph(section, system)
			if !nodes.eligible(name) {
				breaker := nodes.breaker(name)
				_, entry := find(section, breaker)
				found = append(found, fmt.Sprintf("`test.scopes.%s.ci` names %s, where it cannot run: %s runs on %s only (its `systems`)",
					name, system, breaker, joinComma(textsOf(entry["systems"]))))
			}
		}
	}
	return found
}

// cycleOf is a cycle of deps as "a → b → a", or "".
func cycleOf(section map[string]any) string {
	const (
		visiting = 1
		done     = 2
	)
	state := map[string]int{}
	var trail []string
	var visit func(name string) string
	visit = func(name string) string {
		switch state[name] {
		case done:
			return ""
		case visiting:
			start := slices.Index(trail, name)
			return strings.Join(append(slices.Clone(trail[start:]), name), " → ")
		}
		state[name] = visiting
		trail = append(trail, name)
		_, entry := find(section, name)
		for _, dep := range depsOf(entry) {
			if found := visit(dep); found != "" {
				return found
			}
		}
		trail = trail[:len(trail)-1]
		state[name] = done
		return ""
	}
	for _, kind := range []string{"scopes", "steps"} {
		for _, name := range sortedKeys(objectOf(section[kind])) {
			if found := visit(name); found != "" {
				return found
			}
		}
	}
	return ""
}
