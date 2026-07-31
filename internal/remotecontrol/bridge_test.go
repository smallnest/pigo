package remotecontrol

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakeSink struct {
	mu        sync.Mutex
	outputs   []string
	confirms  []confirmReq
	connected bool
}

type confirmReq struct {
	id, tool, summary string
}

func (f *fakeSink) SendOutput(text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.outputs = append(f.outputs, text)
}

func (f *fakeSink) SendConfirm(id, tool, summary string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.confirms = append(f.confirms, confirmReq{id, tool, summary})
}

func (f *fakeSink) HasClient() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connected
}

func (f *fakeSink) lastConfirmID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.confirms) == 0 {
		return ""
	}
	return f.confirms[len(f.confirms)-1].id
}

func TestBridgeOutputWriterTees(t *testing.T) {
	sink := &fakeSink{}
	b := NewBridge(sink)
	w := b.OutputWriter()
	n, err := w.Write([]byte("hello world"))
	if err != nil || n != len("hello world") {
		t.Fatalf("Write = (%d,%v), want (%d,nil)", n, err, len("hello world"))
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.outputs) != 1 || sink.outputs[0] != "hello world" {
		t.Fatalf("outputs = %v, want [hello world]", sink.outputs)
	}
}

func TestBridgeRemoteInput(t *testing.T) {
	b := NewBridge(&fakeSink{})
	b.OnInput("do a thing")
	select {
	case got := <-b.RemoteInput():
		if got != "do a thing" {
			t.Fatalf("input = %q, want 'do a thing'", got)
		}
	case <-time.After(time.Second):
		t.Fatal("no input delivered")
	}
}

func TestBridgeOnInputDropsOldestWhenFull(t *testing.T) {
	b := NewBridge(&fakeSink{})
	// Fill beyond capacity; must not block and must retain the newest items.
	total := remoteInputBuffer + 10
	for i := range total {
		b.OnInput(itoa(uint64(i)))
	}
	// Drain and ensure we get exactly buffer-size items, ending at the newest.
	var got []string
	for {
		select {
		case v := <-b.RemoteInput():
			got = append(got, v)
			continue
		default:
		}
		break
	}
	if len(got) != remoteInputBuffer {
		t.Fatalf("drained %d items, want %d", len(got), remoteInputBuffer)
	}
	if last := got[len(got)-1]; last != itoa(uint64(total-1)) {
		t.Fatalf("newest = %q, want %q", last, itoa(uint64(total-1)))
	}
}

func TestBridgeConfirmResolvedRemotely(t *testing.T) {
	sink := &fakeSink{connected: true}
	b := NewBridge(sink)

	done := make(chan struct {
		d      Decision
		remote bool
	}, 1)
	go func() {
		d, remote := b.Confirm(context.Background(), "shell", "rm -rf /tmp/x")
		done <- struct {
			d      Decision
			remote bool
		}{d, remote}
	}()

	// Wait for the confirm to be sent, then answer it via OnDecide.
	deadline := time.Now().Add(time.Second)
	for sink.lastConfirmID() == "" {
		if time.Now().After(deadline) {
			t.Fatal("confirm never sent")
		}
		time.Sleep(2 * time.Millisecond)
	}
	b.OnDecide(sink.lastConfirmID(), true, true)

	select {
	case r := <-done:
		if !r.remote {
			t.Fatal("remote flag = false, want true")
		}
		if !r.d.Approve || !r.d.Always {
			t.Fatalf("decision = %+v, want approve+always", r.d)
		}
	case <-time.After(time.Second):
		t.Fatal("Confirm did not return")
	}
}

func TestBridgeConfirmCancelledByContext(t *testing.T) {
	sink := &fakeSink{connected: true}
	b := NewBridge(sink)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan bool, 1)
	go func() {
		_, remote := b.Confirm(ctx, "shell", "ls")
		done <- remote
	}()
	// Let the confirm register, then cancel (simulating a local answer).
	deadline := time.Now().Add(time.Second)
	for sink.lastConfirmID() == "" {
		if time.Now().After(deadline) {
			t.Fatal("confirm never sent")
		}
		time.Sleep(2 * time.Millisecond)
	}
	cancel()

	select {
	case remote := <-done:
		if remote {
			t.Fatal("remote flag = true, want false on ctx cancel")
		}
	case <-time.After(time.Second):
		t.Fatal("Confirm did not return on cancel")
	}
}

func TestBridgeResolveConfirmUnknown(t *testing.T) {
	b := NewBridge(&fakeSink{})
	if b.ResolveConfirm("nope", true, false) {
		t.Fatal("ResolveConfirm on unknown id = true, want false")
	}
}

func TestBridgeEnabled(t *testing.T) {
	var nilBridge *Bridge
	if nilBridge.Enabled() {
		t.Fatal("nil bridge Enabled = true, want false")
	}
	sink := &fakeSink{connected: false}
	b := NewBridge(sink)
	if b.Enabled() {
		t.Fatal("Enabled = true with no client, want false")
	}
	sink.connected = true
	if !b.Enabled() {
		t.Fatal("Enabled = false with client, want true")
	}
}
