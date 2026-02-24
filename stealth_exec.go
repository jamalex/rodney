package main

import (
	"errors"
	"fmt"
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

// newStealthCtx creates a new stealthCtx for the given page with sensible defaults.
func newStealthCtx(page *rod.Page) *stealthCtx {
	return &stealthCtx{
		page:     page,
		ctxID:    0,
		cursorX:  640,
		cursorY:  320,
		viewport: [2]int{1920, 935},
	}
}

// getStealthCtx returns the cached stealthCtx for a page, creating one if needed.
func getStealthCtx(page *rod.Page) *stealthCtx {
	key := page.TargetID
	if v, ok := stealthCtxMap.Load(key); ok {
		return v.(*stealthCtx)
	}
	sc := newStealthCtx(page)
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
