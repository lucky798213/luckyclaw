package main

import (
	"context"
	"testing"
	"time"
)

func TestWaitForShutdownCancelsAndWaitsAfterTerminalEOF(t *testing.T) {
	runtimeCtx, runtimeCancel := context.WithCancel(context.Background())
	terminalDone := make(chan struct{})
	gatewayDone := make(chan struct{})
	returned := make(chan struct{})
	go func() {
		waitForShutdown(context.Background(), terminalDone, gatewayDone, runtimeCancel)
		close(returned)
	}()

	close(terminalDone)
	waitForDone(t, runtimeCtx.Done(), "运行上下文没有被取消")
	assertNotDone(t, returned, "Gateway 结束前 waitForShutdown 已经返回")
	close(gatewayDone)
	waitForDone(t, returned, "Gateway 结束后 waitForShutdown 没有返回")
}

func TestWaitForShutdownCancelsAndWaitsAfterSignal(t *testing.T) {
	signalCtx, stopSignal := context.WithCancel(context.Background())
	runtimeCtx, runtimeCancel := context.WithCancel(context.Background())
	terminalDone := make(chan struct{})
	gatewayDone := make(chan struct{})
	returned := make(chan struct{})
	go func() {
		waitForShutdown(signalCtx, terminalDone, gatewayDone, runtimeCancel)
		close(returned)
	}()

	stopSignal()
	waitForDone(t, runtimeCtx.Done(), "运行上下文没有被取消")
	assertNotDone(t, returned, "Gateway 结束前 waitForShutdown 已经返回")
	close(gatewayDone)
	waitForDone(t, returned, "Gateway 结束后 waitForShutdown 没有返回")
}

func TestWaitForShutdownCancelsWhenGatewayStopsUnexpectedly(t *testing.T) {
	runtimeCtx, runtimeCancel := context.WithCancel(context.Background())
	terminalDone := make(chan struct{})
	gatewayDone := make(chan struct{})
	returned := make(chan struct{})
	go func() {
		waitForShutdown(context.Background(), terminalDone, gatewayDone, runtimeCancel)
		close(returned)
	}()

	close(gatewayDone)
	waitForDone(t, runtimeCtx.Done(), "Gateway 退出后运行上下文没有被取消")
	waitForDone(t, returned, "Gateway 退出后 waitForShutdown 没有返回")
}

func waitForDone(t *testing.T, done <-chan struct{}, failure string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal(failure)
	}
}

func assertNotDone(t *testing.T, done <-chan struct{}, failure string) {
	t.Helper()
	select {
	case <-done:
		t.Fatal(failure)
	case <-time.After(50 * time.Millisecond):
	}
}
