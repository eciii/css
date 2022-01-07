// Package css implements CSS selectors for HTML elements.
package css

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// ParseError is returned indicating an lex, parse, or compilation error with
// the associated position in the string the error occurred.
type ParseError struct {
	Pos int
	Msg string
}

// Error returns a formatted version of the error.
func (p *ParseError) Error() string {
	return fmt.Sprintf("css: %s at position %d", p.Msg, p.Pos)
}

func errorf(pos int, msg string, v ...interface{}) error {
	return &ParseError{pos, fmt.Sprintf(msg, v...)}
}

// Selector is a compiled CSS selector.
type Selector struct {
	s []*selector
}

// Select returns any matches from a parsed HTML document.
func (s *Selector) Select(n *html.Node) []*html.Node {
	selected := []*html.Node{}
	for _, sel := range s.s {
		selected = append(selected, sel.find(n)...)
	}
	return selected
}

func findAll(n *html.Node, fn func(n *html.Node) bool) []*html.Node {
	var m []*html.Node
	if fn(n) {
		m = append(m, n)
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode {
			continue
		}
		m = append(m, findAll(c, fn)...)
	}
	return m
}

// MustParse is like Parse but panics on errors.
func MustParse(s string) *Selector {
	sel, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return sel
}

// Parse compiles a complex selector list from a string. The parser supports
// Selectors Level 4.
//
// Multiple selectors are supported through comma separated values. For example
// "h1, h2".
//
// Parse reports the first error hit when compiling.
func Parse(s string) (*Selector, error) {
	p := newParser(s)
	list, err := p.parse()
	if err != nil {
		var perr *parseErr
		if errors.As(err, &perr) {
			return nil, &ParseError{perr.t.pos, perr.msg}
		}
		var lerr *lexErr
		if errors.As(err, &lerr) {
			return nil, &ParseError{lerr.last, lerr.msg}
		}
		return nil, err
	}
	sel := &Selector{}

	c := compiler{maxErrs: 1}
	for _, s := range list {
		m := c.compile(&s)
		if m == nil {
			continue
		}
		sel.s = append(sel.s, m)
	}
	if err := c.err(); err != nil {
		return nil, err
	}
	return sel, nil
}

type compiler struct {
	sels    []complexSelector
	maxErrs int
	errs    []error
}

func (c *compiler) err() error {
	if len(c.errs) == 0 {
		return nil
	}
	return c.errs[0]
}

func (c *compiler) errorf(pos int, msg string, v ...interface{}) bool {
	err := &ParseError{pos, fmt.Sprintf(msg, v...)}
	c.errs = append(c.errs, err)
	if len(c.errs) >= c.maxErrs {
		return true
	}
	return false
}

type selector struct {
	m *compoundSelectorMatcher

	combinators []func(n *html.Node) []*html.Node
}

func (s selector) find(n *html.Node) []*html.Node {
	nodes := findAll(n, s.m.match)
	for _, combinator := range s.combinators {
		var ns []*html.Node
		for _, n := range nodes {
			ns = append(ns, combinator(n)...)
		}
		nodes = ns
	}
	return nodes
}

type descendantCombinator struct {
	m *compoundSelectorMatcher
}

func (c *descendantCombinator) find(n *html.Node) []*html.Node {
	var nodes []*html.Node
	for n := n.FirstChild; n != nil; n = n.NextSibling {
		if n.Type != html.ElementNode {
			continue
		}
		nodes = append(nodes, findAll(n, c.m.match)...)
	}
	return nodes
}

type childCombinator struct {
	m *compoundSelectorMatcher
}

func (c *childCombinator) find(n *html.Node) []*html.Node {
	var nodes []*html.Node
	for n := n.FirstChild; n != nil; n = n.NextSibling {
		if n.Type != html.ElementNode {
			continue
		}
		if c.m.match(n) {
			nodes = append(nodes, n)
		}
	}
	return nodes
}

type adjacentCombinator struct {
	m *compoundSelectorMatcher
}

func (c *adjacentCombinator) find(n *html.Node) []*html.Node {
	var (
		nodes []*html.Node
		prev  *html.Node
		next  *html.Node
	)
	for prev = n.PrevSibling; prev != nil; prev = prev.PrevSibling {
		if prev.Type == html.ElementNode {
			break
		}
	}
	for next = n.NextSibling; next != nil; next = next.NextSibling {
		if next.Type == html.ElementNode {
			break
		}
	}
	if prev != nil && c.m.match(prev) {
		nodes = append(nodes, prev)
	}
	if next != nil && c.m.match(next) {
		nodes = append(nodes, next)
	}
	return nodes
}

type siblingCombinator struct {
	m *compoundSelectorMatcher
}

func (c *siblingCombinator) find(n *html.Node) []*html.Node {
	var nodes []*html.Node
	for n := n.PrevSibling; n != nil; n = n.PrevSibling {
		if n.Type != html.ElementNode {
			continue
		}
		if c.m.match(n) {
			nodes = append(nodes, n)
		}
	}
	for n := n.NextSibling; n != nil; n = n.NextSibling {
		if n.Type != html.ElementNode {
			continue
		}
		if c.m.match(n) {
			nodes = append(nodes, n)
		}
	}
	return nodes
}

func (c *compiler) compile(s *complexSelector) *selector {
	m := &selector{
		m: c.compoundSelector(&s.sel),
	}
	curr := s
	for {
		if curr.next == nil {
			return m
		}
		sel := c.compoundSelector(&curr.next.sel)
		combinator := curr.combinator

		curr = curr.next

		var fn func(n *html.Node) []*html.Node
		switch combinator {
		case "":
			fn = (&descendantCombinator{sel}).find
		case ">":
			fn = (&childCombinator{sel}).find
		case "+":
			fn = (&adjacentCombinator{sel}).find
		case "~":
			fn = (&siblingCombinator{sel}).find
		default:
			c.errorf(curr.pos, "unexpected combinator: %s", combinator)
			continue
		}
		m.combinators = append(m.combinators, fn)
	}
	return m
}

type compoundSelectorMatcher struct {
	m   *typeSelectorMatcher
	scm []subclassSelectorMatcher
}

func (c *compoundSelectorMatcher) match(n *html.Node) bool {
	if c.m != nil {
		if !c.m.match(n) {
			return false
		}
	}
	for _, m := range c.scm {
		if !m.match(n) {
			return false
		}
	}
	return true
}

func (c *compiler) compoundSelector(s *compoundSelector) *compoundSelectorMatcher {
	m := &compoundSelectorMatcher{}
	if s.typeSelector != nil {
		m.m = c.typeSelector(s.typeSelector)
	}
	for _, sc := range s.subClasses {
		scm := c.subclassSelector(&sc)
		if scm != nil {
			m.scm = append(m.scm, *scm)
		}
	}
	if len(s.pseudoSelectors) != 0 {
		// It's not clear that it makes sense for us to support pseudo elements,
		// since this is more about modifying added elements than selecting elements.
		//
		// https://developer.mozilla.org/en-US/docs/Web/CSS/Pseudo-elements
		if c.errorf(s.pos, "pseudo element selectors not supported") {
			return nil
		}
	}
	return m
}

type subclassSelectorMatcher struct {
	idSelector        string
	classSelector     string
	attributeSelector *attributeSelectorMatcher
	pseudoSelector    func(*html.Node) bool
}

func (s *subclassSelectorMatcher) match(n *html.Node) bool {
	if s.idSelector != "" {
		for _, a := range n.Attr {
			if a.Key == "id" && a.Val == s.idSelector {
				return true
			}
		}
		return false
	}

	if s.classSelector != "" {
		for _, a := range n.Attr {
			if a.Key == "class" && a.Val == s.classSelector {
				return true
			}
		}
		return false
	}

	if s.attributeSelector != nil {
		return s.attributeSelector.match(n)
	}

	if s.pseudoSelector != nil {
		return s.pseudoSelector(n)
	}
	return false
}

func (c *compiler) subclassSelector(s *subclassSelector) *subclassSelectorMatcher {
	m := &subclassSelectorMatcher{
		idSelector:    s.idSelector,
		classSelector: s.classSelector,
	}
	if s.attributeSelector != nil {
		m.attributeSelector = c.attributeSelector(s.attributeSelector)
	}
	if s.pseudoClassSelector != nil {
		m.pseudoSelector = c.pseudoClassSelector(s.pseudoClassSelector)
	}
	return m
}

type pseudoClassSelectorMatcher struct {
	matcher func(*html.Node) bool
}

func (c *compiler) pseudoClassSelector(s *pseudoClassSelector) func(*html.Node) bool {
	// https://developer.mozilla.org/en-US/docs/Web/CSS/Pseudo-classes
	switch s.ident {
	case "empty":
		return emptyMatcher
	case "first-child":
		return firstChildMatcher
	case "first-of-type":
		return firstOfTypeMatcher
	case "last-child":
		return lastChildMatcher
	case "last-of-type":
		return lastOfTypeMatcher
	case "only-child":
		return onlyChildMatcher
	case "only-of-type":
		return onlyOfTypeMatcher
	case "root":
		return rootMatcher
	case "":
	default:
		c.errorf(s.pos, "unsupported pseudo-class selector: %s", s.ident)
		return nil
	}

	switch s.function {
	case "nth-child(":
		return c.nthChild(s)
	case "nth-last-child(":
		return c.nthLastChild(s)
	case "nth-last-of-type(":
		return c.nthLastOfType(s)
	case "nth-of-type(":
		return c.nthOfType(s)
	default:
		c.errorf(s.pos, "unsupported pseudo-class selector: %s", s.function)
		return nil
	}

	return nil
}

// https://developer.mozilla.org/en-US/docs/Web/CSS/:nth-child
func (c *compiler) nthChild(s *pseudoClassSelector) func(n *html.Node) bool {
	nth := c.compileNth(s)
	if nth == nil {
		return nil
	}
	return func(n *html.Node) bool {
		var i uint64 = 1
		for s := n.PrevSibling; s != nil; s = s.PrevSibling {
			if s.Type == html.ElementNode {
				i++
			}
		}
		return nth.matches(i)
	}
}

// https://developer.mozilla.org/en-US/docs/Web/CSS/:nth-of-type
func (c *compiler) nthOfType(s *pseudoClassSelector) func(n *html.Node) bool {
	nth := c.compileNth(s)
	if nth == nil {
		return nil
	}
	return func(n *html.Node) bool {
		var i uint64 = 1
		for s := n.PrevSibling; s != nil; s = s.PrevSibling {
			if s.Type == html.ElementNode && s.DataAtom == n.DataAtom {
				i++
			}
		}
		return nth.matches(i)
	}
}

// https://developer.mozilla.org/en-US/docs/Web/CSS/:nth-last-child
func (c *compiler) nthLastChild(s *pseudoClassSelector) func(n *html.Node) bool {
	nth := c.compileNth(s)
	if nth == nil {
		return nil
	}
	return func(n *html.Node) bool {
		var i uint64 = 1
		for s := n.NextSibling; s != nil; s = s.NextSibling {
			if s.Type == html.ElementNode {
				i++
			}
		}
		return nth.matches(i)
	}
}

// https://developer.mozilla.org/en-US/docs/Web/CSS/:nth-last-of-type
func (c *compiler) nthLastOfType(s *pseudoClassSelector) func(n *html.Node) bool {
	nth := c.compileNth(s)
	if nth == nil {
		return nil
	}
	return func(n *html.Node) bool {
		var i uint64 = 1
		for s := n.NextSibling; s != nil; s = s.NextSibling {
			if s.Type == html.ElementNode && n.DataAtom == s.DataAtom {
				i++
			}
		}
		return nth.matches(i)
	}
}

// nth holds a computed An+B value for :nth-child() and its associated selectors.
type nth struct {
	step   uint64 // A
	offset uint64 // B
}

func (nth nth) matches(n uint64) bool {
	if nth.step > n {
		return nth.offset == n
	}
	switch nth.step {
	case 0:
	case 1:
		// n % 1 is always 0, which isn't what we want.
		return n >= nth.offset
	default:
		n = n % nth.step
	}
	return nth.offset == n
}

func (c *compiler) compileNth(s *pseudoClassSelector) *nth {
	n := &nth{}
	seenStep := false
	seenPlus := false
	seenNumber := false
	for _, t := range s.args {
		if t.typ == tokenWhitespace {
			continue
		}

		// Dimentions are like "4n", indicating a step.
		if t.typ == tokenDimension {
			if seenStep || seenPlus {
				c.errorf(t.pos, "expected number")
				return nil
			}
			if seenNumber {
				c.errorf(t.pos, "expected no more arguments")
				return nil
			}
			if !strings.HasSuffix(t.s, "n") {
				c.errorf(t.pos, "expected dimension of form '[0-9]+n' or number")
				return nil
			}
			s := strings.TrimSuffix(t.s, "n")
			step, err := strconv.ParseUint(s, 10, 64)
			if err != nil {
				c.errorf(t.pos, "expected dimension of form '[0-9]+n' or number")
				return nil
			}
			seenStep = true
			n.step = step
			continue
		}

		if t.typ == tokenDelim {
			if seenNumber {
				c.errorf(t.pos, "expected no more arguments")
				return nil
			}
			if !seenStep {
				// Disallow patterns like '(+ +4)'
				c.errorf(t.pos, "expected dimension of form '[0-9]+n' or number")
				return nil
			}
			if seenPlus || t.s != "+" {
				c.errorf(t.pos, "expected number")
				return nil
			}
			seenPlus = true
			continue
		}

		if t.typ == tokenNumber {
			// Allow patterns like '(+4)' or '(4n + +4)'
			s := strings.TrimPrefix(t.s, "+")
			offset, err := strconv.ParseUint(s, 10, 64)
			if err != nil {
				c.errorf(t.pos, "expected non-negative integer")
				return nil
			}
			n.offset = offset
			seenNumber = true
		}
	}

	if !seenNumber && !seenStep {
		c.errorf(s.pos, "no arguments provided")
		return nil
	}
	return n
}

// https://developer.mozilla.org/en-US/docs/Web/CSS/:empty
func emptyMatcher(n *html.Node) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode {
			return false
		}
	}
	return true
}

// https://developer.mozilla.org/en-US/docs/Web/CSS/:first-child
func firstChildMatcher(n *html.Node) bool {
	for s := n.PrevSibling; s != nil; s = s.PrevSibling {
		if s.Type == html.ElementNode {
			return false
		}
	}
	return true
}

// https://developer.mozilla.org/en-US/docs/Web/CSS/:first-of-type
func firstOfTypeMatcher(n *html.Node) bool {
	for s := n.PrevSibling; s != nil; s = s.PrevSibling {
		if s.Type != html.ElementNode {
			continue
		}
		if s.DataAtom == n.DataAtom {
			return false
		}
	}
	return true
}

// https://developer.mozilla.org/en-US/docs/Web/CSS/:last-child
func lastChildMatcher(n *html.Node) bool {
	for s := n.NextSibling; s != nil; s = s.NextSibling {
		if s.Type == html.ElementNode {
			return false
		}
	}
	return true
}

// https://developer.mozilla.org/en-US/docs/Web/CSS/:last-of-type
func lastOfTypeMatcher(n *html.Node) bool {
	for s := n.NextSibling; s != nil; s = s.NextSibling {
		if s.Type != html.ElementNode {
			continue
		}
		if s.DataAtom == n.DataAtom {
			return false
		}
	}
	return true
}

// https://developer.mozilla.org/en-US/docs/Web/CSS/:only-child
func onlyChildMatcher(n *html.Node) bool {
	return firstChildMatcher(n) && lastChildMatcher(n)
}

// https://developer.mozilla.org/en-US/docs/Web/CSS/:only-of-type
func onlyOfTypeMatcher(n *html.Node) bool {
	return firstOfTypeMatcher(n) && lastOfTypeMatcher(n)
}

// https://developer.mozilla.org/en-US/docs/Web/CSS/:root
func rootMatcher(n *html.Node) bool {
	return n.Parent == nil
}

type attributeSelectorMatcher struct {
	ns namespaceMatcher
	fn func(key, val string) bool
}

func (a *attributeSelectorMatcher) match(n *html.Node) bool {
	for _, attr := range n.Attr {
		if a.ns.match(attr.Namespace) && a.fn(attr.Key, attr.Val) {
			return true
		}
	}
	return false
}

func (c *compiler) attributeSelector(s *attributeSelector) *attributeSelectorMatcher {
	m := &attributeSelectorMatcher{
		ns: newNamespaceMatcher(s.wqName.hasPrefix, s.wqName.prefix),
	}
	key := s.wqName.value
	val := s.val

	if s.modifier {
		key = strings.ToLower(key)
		val = strings.ToLower(val)
	}

	// https://developer.mozilla.org/en-US/docs/Web/CSS/Attribute_selectors
	switch s.matcher {
	case "=":
		m.fn = func(k, v string) bool { return k == key && v == val }
	case "~=":
		m.fn = func(k, v string) bool {
			if k != key {
				return false
			}
			for _, f := range strings.Fields(v) {
				if f == val {
					return true
				}
			}
			return false
		}
	case "|=":
		// "Represents elements with an attribute name of attr whose value can be
		// exactly value or can begin with value immediately followed by a hyphen,
		// - (U+002D). It is often used for language subcode matches."
		m.fn = func(k, v string) bool {
			return k == key && (v == val || strings.HasPrefix(v, val+"-"))
		}
	case "^=":
		m.fn = func(k, v string) bool {
			return k == key && strings.HasPrefix(v, val)
		}
	case "$=":
		m.fn = func(k, v string) bool {
			return k == key && strings.HasSuffix(v, val)
		}
	case "*=":
		m.fn = func(k, v string) bool {
			return k == key && strings.Contains(v, val)
		}
	case "":
		m.fn = func(k, v string) bool { return k == key }
	default:
		c.errorf(s.pos, "unsupported attribute matcher: %s", s.matcher)
		return nil
	}
	if s.modifier {
		fn := m.fn
		m.fn = func(k, v string) bool {
			k = strings.ToLower(k)
			v = strings.ToLower(v)
			return fn(k, v)
		}
	}
	return m
}

// namespaceMatcher performs <ns-prefix> matching for elements and attributes.
type namespaceMatcher struct {
	noNamespace bool
	namespace   string
}

func newNamespaceMatcher(hasPrefix bool, prefix string) namespaceMatcher {
	if !hasPrefix {
		return namespaceMatcher{}
	}
	if prefix == "" {
		return namespaceMatcher{noNamespace: true}
	}
	if prefix == "*" {
		return namespaceMatcher{}
	}
	return namespaceMatcher{namespace: prefix}
}

func (n *namespaceMatcher) match(ns string) bool {
	if n.noNamespace {
		return ns == ""
	}
	if n.namespace == "" {
		return true
	}
	return n.namespace == ns
}

type typeSelectorMatcher struct {
	allAtoms bool
	atom     atom.Atom
	ns       namespaceMatcher
}

func (t *typeSelectorMatcher) match(n *html.Node) (ok bool) {
	if !(t.allAtoms || t.atom == n.DataAtom) {
		return false
	}
	return t.ns.match(n.Namespace)
}

func (c *compiler) typeSelector(s *typeSelector) *typeSelectorMatcher {
	m := &typeSelectorMatcher{}
	if s.value == "*" {
		m.allAtoms = true
	} else {
		a := atom.Lookup([]byte(s.value))
		if a == 0 {
			if c.errorf(s.pos, "unrecognized node name: %s", s.value) {
				return nil
			}
		}
		m.atom = a
	}
	m.ns = newNamespaceMatcher(s.hasPrefix, s.prefix)
	return m
}
