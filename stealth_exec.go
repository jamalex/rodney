package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/cdp"
	"github.com/go-rod/rod/lib/proto"
)

// stealthCtx holds per-page state for stealth CDP operations that bypass
// the page's main JavaScript world, preventing detection by monkey-patched
// DOM APIs.
type stealthCtx struct {
	page     *rod.Page
	ctxID    proto.RuntimeExecutionContextID // isolated world context; 0 = not created
	cursorX  float64
	cursorY  float64
	viewport [2]int // [width, height]
}

// stealthCtxMap caches stealthCtx instances keyed by page TargetID.
var stealthCtxMap sync.Map

// newStealthCtx creates a new stealthCtx for the given page.
// Viewport defaults to 1920x935 unless overridden.
func newStealthCtx(page *rod.Page, vpWidth, vpHeight int) *stealthCtx {
	if vpWidth <= 0 || vpHeight <= 0 {
		vpWidth, vpHeight = 1920, 935
	}
	return &stealthCtx{
		page:     page,
		ctxID:    0,
		cursorX:  float64(vpWidth) * 0.33,
		cursorY:  float64(vpHeight) * 0.34,
		viewport: [2]int{vpWidth, vpHeight},
	}
}

// getStealthCtx returns the cached stealthCtx for a page, creating one if needed.
// The State's viewport dimensions are used for new contexts.
func getStealthCtx(page *rod.Page, s *State) *stealthCtx {
	key := page.TargetID
	if v, ok := stealthCtxMap.Load(key); ok {
		return v.(*stealthCtx)
	}
	sc := newStealthCtx(page, s.ViewportWidth, s.ViewportHeight)
	actual, _ := stealthCtxMap.LoadOrStore(key, sc)
	return actual.(*stealthCtx)
}

// ensureIsolatedWorld lazily creates an isolated world for the page's main frame
// and caches the execution context ID. Returns the cached ID if already created.
func (sc *stealthCtx) ensureIsolatedWorld() (proto.RuntimeExecutionContextID, error) {
	if sc.ctxID != 0 {
		return sc.ctxID, nil
	}

	result, err := proto.PageCreateIsolatedWorld{
		FrameID:   sc.page.FrameID,
		WorldName: "rodney",
	}.Call(sc.page)
	if err != nil {
		return 0, fmt.Errorf("failed to create isolated world: %w", err)
	}

	sc.ctxID = result.ExecutionContextID
	return sc.ctxID, nil
}

// invalidateWorld resets the cached context ID (e.g. after navigation).
func (sc *stealthCtx) invalidateWorld() {
	sc.ctxID = 0
}

// isStaleContextError returns true if the error indicates a stale execution context.
func isStaleContextError(err error) bool {
	if err == nil {
		return false
	}
	var cdpErr *cdp.Error
	if errors.As(err, &cdpErr) {
		if cdpErr.Is(cdp.ErrCtxNotFound) || cdpErr.Is(cdp.ErrCtxDestroyed) {
			return true
		}
	}
	return false
}

// getDocRoot calls DOM.getDocument and returns the root NodeID.
func (sc *stealthCtx) getDocRoot() (proto.DOMNodeID, error) {
	result, err := proto.DOMGetDocument{}.Call(sc.page)
	if err != nil {
		return 0, fmt.Errorf("DOM.getDocument failed: %w", err)
	}
	return result.Root.NodeID, nil
}

// element polls DOM.querySelector until the element is found or timeout is reached.
// Returns the DOMNodeID of the matched element.
func (sc *stealthCtx) element(selector string, timeout time.Duration) (proto.DOMNodeID, error) {
	deadline := time.Now().Add(timeout)

	for {
		rootID, err := sc.getDocRoot()
		if err != nil {
			return 0, err
		}

		result, err := proto.DOMQuerySelector{
			NodeID:   rootID,
			Selector: selector,
		}.Call(sc.page)
		if err != nil {
			return 0, fmt.Errorf("DOM.querySelector failed: %w", err)
		}

		if result.NodeID != 0 {
			return result.NodeID, nil
		}

		if time.Now().After(deadline) {
			return 0, fmt.Errorf("element %q not found within %v", selector, timeout)
		}

		time.Sleep(100 * time.Millisecond)
	}
}

// elements polls DOM.querySelectorAll until at least one element is found or timeout is reached.
// If timeout is reached with no results, returns (nil, nil) (empty, no error).
func (sc *stealthCtx) elements(selector string, timeout time.Duration) ([]proto.DOMNodeID, error) {
	deadline := time.Now().Add(timeout)

	for {
		rootID, err := sc.getDocRoot()
		if err != nil {
			return nil, err
		}

		result, err := proto.DOMQuerySelectorAll{
			NodeID:   rootID,
			Selector: selector,
		}.Call(sc.page)
		if err != nil {
			return nil, fmt.Errorf("DOM.querySelectorAll failed: %w", err)
		}

		if len(result.NodeIDs) > 0 {
			return result.NodeIDs, nil
		}

		if time.Now().After(deadline) {
			return nil, nil
		}

		time.Sleep(100 * time.Millisecond)
	}
}

// eval evaluates a JavaScript expression in the isolated world.
// On stale context error, invalidates the world and retries once.
func (sc *stealthCtx) eval(expr string) (*proto.RuntimeEvaluateResult, error) {
	ctxID, err := sc.ensureIsolatedWorld()
	if err != nil {
		return nil, err
	}

	result, err := proto.RuntimeEvaluate{
		Expression:    expr,
		ContextID:     ctxID,
		ReturnByValue: true,
		AwaitPromise:  true,
	}.Call(sc.page)

	if isStaleContextError(err) {
		sc.invalidateWorld()
		ctxID, err = sc.ensureIsolatedWorld()
		if err != nil {
			return nil, err
		}
		result, err = proto.RuntimeEvaluate{
			Expression:    expr,
			ContextID:     ctxID,
			ReturnByValue: true,
			AwaitPromise:  true,
		}.Call(sc.page)
	}
	if err != nil {
		return nil, fmt.Errorf("Runtime.evaluate failed: %w", err)
	}

	if result.ExceptionDetails != nil {
		return nil, fmt.Errorf("JS exception: %s", result.ExceptionDetails.Text)
	}

	return result, nil
}

// callOn resolves a DOM node into the isolated world and calls a function on it.
// On stale context error, invalidates the world and retries once.
func (sc *stealthCtx) callOn(nodeID proto.DOMNodeID, fn string) (*proto.RuntimeCallFunctionOnResult, error) {
	ctxID, err := sc.ensureIsolatedWorld()
	if err != nil {
		return nil, err
	}

	resolved, err := sc.resolveNode(nodeID, ctxID)
	if isStaleContextError(err) {
		sc.invalidateWorld()
		ctxID, err = sc.ensureIsolatedWorld()
		if err != nil {
			return nil, err
		}
		resolved, err = sc.resolveNode(nodeID, ctxID)
	}
	if err != nil {
		return nil, fmt.Errorf("DOM.resolveNode failed: %w", err)
	}

	result, err := proto.RuntimeCallFunctionOn{
		ObjectID:            resolved.Object.ObjectID,
		FunctionDeclaration: fn,
		ReturnByValue:       true,
	}.Call(sc.page)

	if isStaleContextError(err) {
		sc.invalidateWorld()
		ctxID, err = sc.ensureIsolatedWorld()
		if err != nil {
			return nil, err
		}
		resolved, err = sc.resolveNode(nodeID, ctxID)
		if err != nil {
			return nil, fmt.Errorf("DOM.resolveNode retry failed: %w", err)
		}
		result, err = proto.RuntimeCallFunctionOn{
			ObjectID:            resolved.Object.ObjectID,
			FunctionDeclaration: fn,
			ReturnByValue:       true,
		}.Call(sc.page)
	}
	if err != nil {
		return nil, fmt.Errorf("Runtime.callFunctionOn failed: %w", err)
	}

	if result.ExceptionDetails != nil {
		return nil, fmt.Errorf("JS exception: %s", result.ExceptionDetails.Text)
	}

	return result, nil
}

// resolveNode resolves a DOM node into a specific execution context.
func (sc *stealthCtx) resolveNode(nodeID proto.DOMNodeID, ctxID proto.RuntimeExecutionContextID) (*proto.DOMResolveNodeResult, error) {
	return proto.DOMResolveNode{
		NodeID:             nodeID,
		ExecutionContextID: ctxID,
	}.Call(sc.page)
}

// exists does an instant check (no polling/wait) whether a CSS selector matches any element.
// Uses pure CDP DOM.querySelector — no JavaScript execution.
func (sc *stealthCtx) exists(selector string) (bool, error) {
	rootID, err := sc.getDocRoot()
	if err != nil {
		return false, err
	}
	result, err := proto.DOMQuerySelector{
		NodeID:   rootID,
		Selector: selector,
	}.Call(sc.page)
	if err != nil {
		return false, fmt.Errorf("DOM.querySelector failed: %w", err)
	}
	return result.NodeID != 0, nil
}

// count does an instant check returning how many elements match a CSS selector.
// Uses pure CDP DOM.querySelectorAll — no JavaScript execution.
func (sc *stealthCtx) count(selector string) (int, error) {
	rootID, err := sc.getDocRoot()
	if err != nil {
		return 0, err
	}
	result, err := proto.DOMQuerySelectorAll{
		NodeID:   rootID,
		Selector: selector,
	}.Call(sc.page)
	if err != nil {
		return 0, fmt.Errorf("DOM.querySelectorAll failed: %w", err)
	}
	return len(result.NodeIDs), nil
}

// attr retrieves a single attribute value from a DOM node by name.
// Uses pure CDP DOM.getAttributes — no JavaScript execution.
// Returns an error if the attribute is not found on the node.
func (sc *stealthCtx) attr(nodeID proto.DOMNodeID, name string) (string, error) {
	result, err := proto.DOMGetAttributes{
		NodeID: nodeID,
	}.Call(sc.page)
	if err != nil {
		return "", fmt.Errorf("DOM.getAttributes failed: %w", err)
	}
	// Attributes come as a flat [name, value, name, value, ...] array.
	for i := 0; i+1 < len(result.Attributes); i += 2 {
		if result.Attributes[i] == name {
			return result.Attributes[i+1], nil
		}
	}
	return "", fmt.Errorf("attribute %q not found on node %d", name, nodeID)
}

// outerHTML retrieves the outer HTML markup of a DOM node.
// Uses pure CDP DOM.getOuterHTML — no JavaScript execution.
func (sc *stealthCtx) outerHTML(nodeID proto.DOMNodeID) (string, error) {
	result, err := proto.DOMGetOuterHTML{
		NodeID: nodeID,
	}.Call(sc.page)
	if err != nil {
		return "", fmt.Errorf("DOM.getOuterHTML failed: %w", err)
	}
	return result.OuterHTML, nil
}

// text retrieves the innerText of a DOM node by calling a function on it
// in the isolated world via callOn.
func (sc *stealthCtx) text(nodeID proto.DOMNodeID) (string, error) {
	result, err := sc.callOn(nodeID, "function() { return this.innerText; }")
	if err != nil {
		return "", err
	}
	return result.Result.Value.Str(), nil
}

// visible checks whether a DOM node is visible by inspecting computed styles
// and bounding rect in the isolated world via callOn.
// Returns false if the element has display:none, visibility:hidden, opacity:0,
// or zero width/height.
func (sc *stealthCtx) visible(nodeID proto.DOMNodeID) (bool, error) {
	result, err := sc.callOn(nodeID, `function() {
		var style = window.getComputedStyle(this);
		if (style.display === 'none') return false;
		if (style.visibility === 'hidden') return false;
		if (style.opacity === '0') return false;
		var rect = this.getBoundingClientRect();
		if (rect.width <= 0 || rect.height <= 0) return false;
		return true;
	}`)
	if err != nil {
		return false, err
	}
	return result.Result.Value.Bool(), nil
}

// focus sets focus on a DOM node.
// Uses pure CDP DOM.focus — no JavaScript execution.
func (sc *stealthCtx) focus(nodeID proto.DOMNodeID) error {
	err := proto.DOMFocus{
		NodeID: nodeID,
	}.Call(sc.page)
	if err != nil {
		return fmt.Errorf("DOM.focus failed: %w", err)
	}
	return nil
}

// typeChar dispatches keyDown + keyUp for a single character.
func (sc *stealthCtx) typeChar(ch rune) error {
	text := string(ch)
	err := proto.InputDispatchKeyEvent{
		Type: proto.InputDispatchKeyEventTypeKeyDown,
		Key:  text,
		Text: text,
	}.Call(sc.page)
	if err != nil {
		return err
	}
	err = proto.InputDispatchKeyEvent{
		Type: proto.InputDispatchKeyEventTypeKeyUp,
		Key:  text,
	}.Call(sc.page)
	return err
}

// input focuses the element and types text with human-like timing.
func (sc *stealthCtx) input(nodeID proto.DOMNodeID, text string) error {
	if err := sc.focus(nodeID); err != nil {
		return err
	}
	for i, ch := range text {
		if err := sc.typeChar(ch); err != nil {
			return fmt.Errorf("typing failed at char %d: %w", i, err)
		}
		// Human-like delay: 50-150ms, with occasional longer pauses
		delay := 50 + rand.Intn(100)
		if rand.Float64() < 0.1 {
			delay += 100 + rand.Intn(200) // ~10% chance of hesitation
		}
		time.Sleep(time.Duration(delay) * time.Millisecond)
	}
	return nil
}

// clearInput selects all text in the element and deletes it.
func (sc *stealthCtx) clearInput(nodeID proto.DOMNodeID) error {
	if err := sc.focus(nodeID); err != nil {
		return err
	}

	// Select all text in the input using the isolated world
	_, err := sc.callOn(nodeID, "function() { this.select(); }")
	if err != nil {
		return fmt.Errorf("select failed: %w", err)
	}

	// Delete selected text using rawKeyDown (required for special keys)
	err = proto.InputDispatchKeyEvent{
		Type:                  proto.InputDispatchKeyEventTypeRawKeyDown,
		Key:                   "Backspace",
		Code:                  "Backspace",
		WindowsVirtualKeyCode: 8,
		NativeVirtualKeyCode:  8,
	}.Call(sc.page)
	if err != nil {
		return fmt.Errorf("backspace failed: %w", err)
	}
	proto.InputDispatchKeyEvent{
		Type: proto.InputDispatchKeyEventTypeKeyUp,
		Key:  "Backspace",
		Code: "Backspace",
	}.Call(sc.page)
	return nil
}

// easeInOutCubic is an easing function that starts slow, speeds up, then slows again.
// t=0 → 0, t=0.5 → ~0.5, t=1 → 1
func easeInOutCubic(t float64) float64 {
	if t < 0.5 {
		return 4 * t * t * t
	}
	return 1 - math.Pow(-2*t+2, 3)/2
}

// bezierPath generates points along a cubic Bezier curve from (x0,y0) to (x1,y1).
// numSteps controls how many intermediate points to generate.
// Control points are randomized slightly for a natural feel.
func bezierPath(x0, y0, x1, y1 float64, numSteps int) [][2]float64 {
	dx := x1 - x0
	dy := y1 - y0

	// Random control points offset perpendicular to the line
	offset1 := (rand.Float64() - 0.5) * math.Abs(dy) * 0.5
	offset2 := (rand.Float64() - 0.5) * math.Abs(dy) * 0.5

	// Control point 1: ~30% along the line, offset perpendicular
	cp1x := x0 + dx*0.3 + offset1
	cp1y := y0 + dy*0.3 + offset2

	// Control point 2: ~70% along the line, offset perpendicular
	offset3 := (rand.Float64() - 0.5) * math.Abs(dy) * 0.3
	offset4 := (rand.Float64() - 0.5) * math.Abs(dx) * 0.3
	cp2x := x0 + dx*0.7 + offset3
	cp2y := y0 + dy*0.7 + offset4

	points := make([][2]float64, numSteps+1)
	for i := 0; i <= numSteps; i++ {
		t := float64(i) / float64(numSteps)
		u := 1 - t
		// Cubic Bezier: B(t) = (1-t)^3*P0 + 3*(1-t)^2*t*P1 + 3*(1-t)*t^2*P2 + t^3*P3
		x := u*u*u*x0 + 3*u*u*t*cp1x + 3*u*t*t*cp2x + t*t*t*x1
		y := u*u*u*y0 + 3*u*u*t*cp1y + 3*u*t*t*cp2y + t*t*t*y1
		points[i] = [2]float64{x, y}
	}
	// Ensure exact endpoints
	points[0] = [2]float64{x0, y0}
	points[numSteps] = [2]float64{x1, y1}
	return points
}

// scrollOffset returns the current page scroll position (scrollX, scrollY) via the isolated world.
func (sc *stealthCtx) scrollOffset() (float64, float64, error) {
	result, err := sc.eval("[window.scrollX, window.scrollY]")
	if err != nil {
		return 0, 0, fmt.Errorf("failed to get scroll offset: %w", err)
	}
	arr, ok := result.Result.Value.Raw().([]interface{})
	if !ok || len(arr) < 2 {
		return 0, 0, nil
	}
	sx, _ := arr[0].(float64)
	sy, _ := arr[1].(float64)
	return sx, sy, nil
}

// getBoxModelCenter returns the viewport-relative center coordinates of a DOM node's content box.
// DOM.getBoxModel returns document-relative coordinates, so we subtract the current scroll offset.
func (sc *stealthCtx) getBoxModelCenter(nodeID proto.DOMNodeID) (float64, float64, error) {
	result, err := proto.DOMGetBoxModel{NodeID: nodeID}.Call(sc.page)
	if err != nil {
		return 0, 0, fmt.Errorf("DOM.getBoxModel failed: %w", err)
	}
	q := result.Model.Content
	if len(q) < 8 {
		return 0, 0, fmt.Errorf("invalid content quad: got %d values", len(q))
	}
	docX := (q[0] + q[2] + q[4] + q[6]) / 4
	docY := (q[1] + q[3] + q[5] + q[7]) / 4

	scrollX, scrollY, err := sc.scrollOffset()
	if err != nil {
		return 0, 0, err
	}
	return docX - scrollX, docY - scrollY, nil
}

// moveMouse simulates human-like mouse movement from current cursor to (x, y).
// Uses a Bezier curve with eased timing.
func (sc *stealthCtx) moveMouse(x, y float64) error {
	dist := math.Sqrt(math.Pow(x-sc.cursorX, 2) + math.Pow(y-sc.cursorY, 2))

	// Scale number of steps to distance (more steps = smoother for longer moves)
	steps := int(math.Max(10, math.Min(50, dist/10)))
	points := bezierPath(sc.cursorX, sc.cursorY, x, y, steps)

	for i, pt := range points {
		err := proto.InputDispatchMouseEvent{
			Type: proto.InputDispatchMouseEventTypeMouseMoved,
			X:    pt[0],
			Y:    pt[1],
		}.Call(sc.page)
		if err != nil {
			return fmt.Errorf("mouse move failed: %w", err)
		}
		// Eased delay between steps
		if i < len(points)-1 {
			t := float64(i) / float64(len(points)-1)
			// Base delay scales with distance; ease makes it slower at ends
			baseDelay := dist / 400.0 // ~400px/s for normal speed
			delay := baseDelay * (1 + 0.5*(1-math.Abs(2*easeInOutCubic(t)-1)))
			time.Sleep(time.Duration(delay * float64(time.Millisecond)))
		}
	}
	sc.cursorX = x
	sc.cursorY = y
	return nil
}

// scrollIntoView scrolls the page so that the given node is visible in the viewport.
// Uses wheel events for a natural scroll appearance.
func (sc *stealthCtx) scrollIntoView(nodeID proto.DOMNodeID) error {
	box, err := proto.DOMGetBoxModel{NodeID: nodeID}.Call(sc.page)
	if err != nil {
		// Element may not have a box model (e.g., display:none); skip scroll
		return nil
	}
	q := box.Model.Content
	if len(q) < 8 {
		return nil
	}
	// DOM.getBoxModel returns document-relative coordinates
	docY := (q[1] + q[3] + q[5] + q[7]) / 4
	_, scrollY, err := sc.scrollOffset()
	if err != nil {
		return err
	}
	// Convert to viewport-relative
	viewY := docY - scrollY
	vpH := float64(sc.viewport[1])

	// If element center is outside viewport, scroll
	if viewY < 0 || viewY > vpH {
		// Scroll so element is centered: need to scroll by (viewY - vpH/2)
		remaining := viewY - vpH/2
		for math.Abs(remaining) > 10 {
			step := remaining * 0.3 // ease toward target
			if math.Abs(step) < 20 {
				step = math.Copysign(20, remaining)
			}
			err := proto.InputDispatchMouseEvent{
				Type:   proto.InputDispatchMouseEventTypeMouseWheel,
				X:      sc.cursorX,
				Y:      sc.cursorY,
				DeltaX: 0,
				DeltaY: step,
			}.Call(sc.page)
			if err != nil {
				return fmt.Errorf("scroll failed: %w", err)
			}
			remaining -= step
			time.Sleep(30 * time.Millisecond)
		}
		// Brief settle time
		time.Sleep(50 * time.Millisecond)
	}
	return nil
}

// click scrolls to the element, moves the mouse to it, and clicks.
func (sc *stealthCtx) click(nodeID proto.DOMNodeID) error {
	if err := sc.scrollIntoView(nodeID); err != nil {
		return err
	}
	x, y, err := sc.getBoxModelCenter(nodeID)
	if err != nil {
		return err
	}
	if err := sc.moveMouse(x, y); err != nil {
		return err
	}
	// Press
	err = proto.InputDispatchMouseEvent{
		Type:       proto.InputDispatchMouseEventTypeMousePressed,
		X:          x,
		Y:          y,
		Button:     proto.InputMouseButtonLeft,
		ClickCount: 1,
	}.Call(sc.page)
	if err != nil {
		return fmt.Errorf("mouse press failed: %w", err)
	}
	// Brief natural delay between press and release (50-200ms, matching real human clicks)
	time.Sleep(time.Duration(50+rand.Intn(150)) * time.Millisecond)
	// Release
	err = proto.InputDispatchMouseEvent{
		Type:       proto.InputDispatchMouseEventTypeMouseReleased,
		X:          x,
		Y:          y,
		Button:     proto.InputMouseButtonLeft,
		ClickCount: 1,
	}.Call(sc.page)
	if err != nil {
		return fmt.Errorf("mouse release failed: %w", err)
	}
	return nil
}

// hover scrolls to the element and moves the mouse over it.
func (sc *stealthCtx) hover(nodeID proto.DOMNodeID) error {
	if err := sc.scrollIntoView(nodeID); err != nil {
		return err
	}
	x, y, err := sc.getBoxModelCenter(nodeID)
	if err != nil {
		return err
	}
	return sc.moveMouse(x, y)
}

// selectOption sets the value of a <select> element and dispatches a change event.
// Uses callOn to execute in the isolated world.
func (sc *stealthCtx) selectOption(nodeID proto.DOMNodeID, value string) error {
	_, err := sc.callOn(nodeID, fmt.Sprintf(`function() {
		this.value = %q;
		this.dispatchEvent(new Event('change', {bubbles: true}));
	}`, value))
	return err
}

// submit calls submit() on a <form> element via the isolated world.
func (sc *stealthCtx) submit(nodeID proto.DOMNodeID) error {
	_, err := sc.callOn(nodeID, "function() { this.submit(); }")
	return err
}

// download fetches the resource referenced by a node's href or src attribute.
// For data: URLs, decodes inline. For HTTP URLs, fetches via the isolated world.
func (sc *stealthCtx) download(nodeID proto.DOMNodeID) ([]byte, error) {
	// Try href first, then src
	urlStr, err := sc.attr(nodeID, "href")
	if err != nil {
		urlStr, err = sc.attr(nodeID, "src")
		if err != nil {
			return nil, fmt.Errorf("element has no href or src attribute")
		}
	}

	if strings.HasPrefix(urlStr, "data:") {
		return decodeDataURL(urlStr)
	}

	// Fetch via isolated world eval with async fetch
	result, err := sc.eval(fmt.Sprintf(`(async () => {
		const resp = await fetch(%q);
		if (!resp.ok) throw new Error('HTTP ' + resp.status);
		const buf = await resp.arrayBuffer();
		const bytes = new Uint8Array(buf);
		let binary = '';
		for (let i = 0; i < bytes.length; i++) {
			binary += String.fromCharCode(bytes[i]);
		}
		return btoa(binary);
	})()`, urlStr))
	if err != nil {
		return nil, fmt.Errorf("fetch failed: %w", err)
	}

	return base64.StdEncoding.DecodeString(result.Result.Value.Str())
}

// screenshotElement captures a PNG screenshot of a single DOM element.
// Uses the border quad from DOM.getBoxModel to compute the clip region.
func (sc *stealthCtx) screenshotElement(nodeID proto.DOMNodeID) ([]byte, error) {
	box, err := proto.DOMGetBoxModel{NodeID: nodeID}.Call(sc.page)
	if err != nil {
		return nil, fmt.Errorf("DOM.getBoxModel failed: %w", err)
	}

	// Use the border quad (not content) for the screenshot clip
	q := box.Model.Border
	if len(q) < 8 {
		return nil, fmt.Errorf("invalid border quad: got %d values", len(q))
	}

	// Compute bounding rect from the quad's 4 corners
	minX := math.Min(math.Min(q[0], q[2]), math.Min(q[4], q[6]))
	maxX := math.Max(math.Max(q[0], q[2]), math.Max(q[4], q[6]))
	minY := math.Min(math.Min(q[1], q[3]), math.Min(q[5], q[7]))
	maxY := math.Max(math.Max(q[1], q[3]), math.Max(q[5], q[7]))

	format := proto.PageCaptureScreenshotFormatPng
	result, err := proto.PageCaptureScreenshot{
		Format: format,
		Clip: &proto.PageViewport{
			X:      minX,
			Y:      minY,
			Width:  maxX - minX,
			Height: maxY - minY,
			Scale:  1,
		},
	}.Call(sc.page)
	if err != nil {
		return nil, fmt.Errorf("Page.captureScreenshot failed: %w", err)
	}

	// result.Data is already decoded from base64 by the JSON unmarshaler
	return result.Data, nil
}
