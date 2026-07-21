package eqoa

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestParseGroupMemberBody(t *testing.T) {
	buf := new(bytes.Buffer)
	wu := func(v uint32) { binary.Write(buf, binary.LittleEndian, v) }
	wf := func(v float32) { binary.Write(buf, binary.LittleEndian, v) }
	wu(2) // count
	// member 0: dictID, rot(0,0,0), scale 1, pos(0,1.9,0) — a torch flame offset.
	wu(0xAAAA)
	wf(0); wf(0); wf(0)
	wf(1)
	wf(0); wf(1.9); wf(0)
	// member 1: dictID, rot(.1,.2,.3), scale 2, pos(-0.6,0.4,0)
	wu(0xBBBB)
	wf(0.1); wf(0.2); wf(0.3)
	wf(2)
	wf(-0.6); wf(0.4); wf(0)

	m := parseGroupMemberBody(buf.Bytes(), binary.LittleEndian)
	if len(m) != 2 {
		t.Fatalf("members = %d, want 2", len(m))
	}
	if m[0].DictID != 0xAAAA || m[0].Pos != [3]float32{0, 1.9, 0} || m[0].Scale != 1 {
		t.Errorf("member0 = %+v", m[0])
	}
	if m[1].DictID != 0xBBBB || m[1].Rot != [3]float32{0.1, 0.2, 0.3} || m[1].Scale != 2 || m[1].Pos != [3]float32{-0.6, 0.4, 0} {
		t.Errorf("member1 = %+v", m[1])
	}
}

func TestParseGroupMemberBodyGuards(t *testing.T) {
	if parseGroupMemberBody([]byte{1, 2}, binary.LittleEndian) != nil {
		t.Error("short body should return nil")
	}
	// count claims 100 members but body is truncated — stop at what fits, no panic.
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, int32(100))
	buf.Write(make([]byte, 32)) // exactly one member
	if got := len(parseGroupMemberBody(buf.Bytes(), binary.LittleEndian)); got != 1 {
		t.Errorf("truncated: got %d members, want 1", got)
	}
}
