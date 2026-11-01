package transport

import (
	"bytes"
	"testing"
)

func TestDFTPv3CodecRoundtrip(t *testing.T) {
	payload := []byte("DualFlow Phase 7 DFTP v3 Wire Test Payload")
	sessionID := uint32(0x98765432)
	seq := uint64(100500)
	pathID := uint16(2)

	encoded, err := EncodeFrameV3(MsgTypeData, FlagEncrypted, sessionID, seq, pathID, payload)
	if err != nil {
		t.Fatalf("EncodeFrameV3 failed: %v", err)
	}

	if len(encoded) != int(HeaderLength)+len(payload) {
		t.Fatalf("Encoded frame size mismatch: %d != %d", len(encoded), int(HeaderLength)+len(payload))
	}

	decoded, err := DecodeFrame(encoded)
	if err != nil {
		t.Fatalf("DecodeFrame failed: %v", err)
	}

	if decoded.Header.Version != ProtocolVersion3 {
		t.Errorf("Version mismatch: got %d, want %d", decoded.Header.Version, ProtocolVersion3)
	}
	if decoded.Header.MsgType != MsgTypeData {
		t.Errorf("MsgType mismatch: got %d, want %d", decoded.Header.MsgType, MsgTypeData)
	}
	if decoded.Header.SessionID != sessionID {
		t.Errorf("SessionID mismatch: got %x, want %x", decoded.Header.SessionID, sessionID)
	}
	if decoded.Header.SequenceNumber != seq {
		t.Errorf("SequenceNumber mismatch: got %d, want %d", decoded.Header.SequenceNumber, seq)
	}
	if decoded.Header.PathID != pathID {
		t.Errorf("PathID mismatch: got %d, want %d", decoded.Header.PathID, pathID)
	}
	if !bytes.Equal(decoded.Payload, payload) {
		t.Errorf("Payload mismatch: got %s, want %s", string(decoded.Payload), string(payload))
	}
}

func TestDFTPv1BackwardCompatibility(t *testing.T) {
	payload := []byte("Legacy Version 1 Payload with CRC32")
	sessionID := uint32(0x11223344)
	seq := uint64(42)

	encoded, err := EncodeFrameV1(MsgTypeData, 0, sessionID, seq, payload)
	if err != nil {
		t.Fatalf("EncodeFrameV1 failed: %v", err)
	}

	decoded, err := DecodeFrame(encoded)
	if err != nil {
		t.Fatalf("DecodeFrame v1 failed: %v", err)
	}

	if decoded.Header.Version != ProtocolVersion1 {
		t.Errorf("Expected version 1, got: %d", decoded.Header.Version)
	}

	// Corrupt Checksum
	corrupted := make([]byte, len(encoded))
	copy(corrupted, encoded)
	corrupted[30] ^= 0xFF
	_, err = DecodeFrame(corrupted)
	if err != ErrChecksumMismatch {
		t.Errorf("Expected ErrChecksumMismatch, got: %v", err)
	}
}

func TestInvalidHeaderHandling(t *testing.T) {
	valid, _ := EncodeFrameV3(MsgTypeData, 0, 1, 1, 1, []byte("test"))

	// 1. Incomplete Header
	_, err := DecodeHeader(valid[:20])
	if err != ErrIncompleteHeader {
		t.Errorf("Expected ErrIncompleteHeader, got: %v", err)
	}

	// 2. Invalid Magic
	badMagic := make([]byte, len(valid))
	copy(badMagic, valid)
	badMagic[0] = 'X'
	_, err = DecodeHeader(badMagic)
	if err != ErrInvalidMagic {
		t.Errorf("Expected ErrInvalidMagic, got: %v", err)
	}

	// 3. Unsupported Version
	badVer := make([]byte, len(valid))
	copy(badVer, valid)
	badVer[4] = 99
	_, err = DecodeHeader(badVer)
	if err != ErrUnsupportedVersion {
		t.Errorf("Expected ErrUnsupportedVersion, got: %v", err)
	}

	// 4. Invalid HeaderLength
	badHdrLen := make([]byte, len(valid))
	copy(badHdrLen, valid)
	badHdrLen[8] = 0
	badHdrLen[9] = 16
	_, err = DecodeHeader(badHdrLen)
	if err != ErrInvalidHeaderLength {
		t.Errorf("Expected ErrInvalidHeaderLength, got: %v", err)
	}
}

func TestStreamFrameCodec(t *testing.T) {
	flowID := uint32(101)
	offset := uint64(2048)
	flags := FlagStreamSYN | FlagStreamACK
	chunk := make([]byte, 1280)
	for i := range chunk {
		chunk[i] = byte(i % 256)
	}

	encoded, err := EncodeStreamFrame(flowID, offset, flags, chunk)
	if err != nil {
		t.Fatalf("EncodeStreamFrame failed: %v", err)
	}

	if len(encoded) != InnerStreamHeaderLen+len(chunk) {
		t.Fatalf("Stream frame size mismatch: %d != %d", len(encoded), InnerStreamHeaderLen+len(chunk))
	}

	decoded, err := DecodeStreamFrame(encoded)
	if err != nil {
		t.Fatalf("DecodeStreamFrame failed: %v", err)
	}

	if decoded.Header.FlowID != flowID {
		t.Errorf("FlowID mismatch: got %d, want %d", decoded.Header.FlowID, flowID)
	}
	if decoded.Header.StreamOffset != offset {
		t.Errorf("Offset mismatch: got %d, want %d", decoded.Header.StreamOffset, offset)
	}
	if decoded.Header.StreamFlags != flags {
		t.Errorf("Flags mismatch: got %x, want %x", decoded.Header.StreamFlags, flags)
	}
	if !bytes.Equal(decoded.Data, chunk) {
		t.Errorf("Chunk data mismatch")
	}

	// Test chunk size limit enforcement
	tooBig := make([]byte, 1281)
	_, err = EncodeStreamFrame(flowID, offset, flags, tooBig)
	if err != ErrChunkTooLarge {
		t.Errorf("Expected ErrChunkTooLarge for 1281-byte chunk, got: %v", err)
	}
}

func TestStreamAckCodec(t *testing.T) {
	flowID := uint32(50)
	cumAck := uint64(8192)
	blocks := []SackBlock{
		{StartOffset: 9472, EndOffset: 10752},
		{StartOffset: 12032, EndOffset: 13312},
	}

	encoded, err := EncodeStreamAck(flowID, cumAck, blocks)
	if err != nil {
		t.Fatalf("EncodeStreamAck failed: %v", err)
	}

	decoded, err := DecodeStreamAck(encoded)
	if err != nil {
		t.Fatalf("DecodeStreamAck failed: %v", err)
	}

	if decoded.FlowID != flowID {
		t.Errorf("FlowID mismatch: got %d, want %d", decoded.FlowID, flowID)
	}
	if decoded.CumulativeAckOffset != cumAck {
		t.Errorf("CumAck mismatch: got %d, want %d", decoded.CumulativeAckOffset, cumAck)
	}
	if decoded.NumSackBlocks != 2 {
		t.Errorf("NumSackBlocks mismatch: got %d, want 2", decoded.NumSackBlocks)
	}
	if len(decoded.SackBlocks) != 2 {
		t.Fatalf("SackBlocks length mismatch: %d != 2", len(decoded.SackBlocks))
	}
	if decoded.SackBlocks[0].StartOffset != 9472 || decoded.SackBlocks[0].EndOffset != 10752 {
		t.Errorf("Sack block 0 mismatch: %+v", decoded.SackBlocks[0])
	}
	if decoded.SackBlocks[1].StartOffset != 12032 || decoded.SackBlocks[1].EndOffset != 13312 {
		t.Errorf("Sack block 1 mismatch: %+v", decoded.SackBlocks[1])
	}

	// Enforce 8 SACK block ceiling
	tooManyBlocks := make([]SackBlock, 9)
	_, err = EncodeStreamAck(flowID, cumAck, tooManyBlocks)
	if err != ErrTooManySackBlocks {
		t.Errorf("Expected ErrTooManySackBlocks, got: %v", err)
	}
}

func TestFlowOpenAndAckCodec(t *testing.T) {
	flowID := uint32(77)
	port := uint16(5201)
	addr := "192.168.1.50"

	encoded, err := EncodeFlowOpen(flowID, port, AddrTypeIPv4, addr)
	if err != nil {
		t.Fatalf("EncodeFlowOpen failed: %v", err)
	}

	decoded, err := DecodeFlowOpen(encoded)
	if err != nil {
		t.Fatalf("DecodeFlowOpen failed: %v", err)
	}

	if decoded.FlowID != flowID || decoded.Port != port || decoded.AddrType != AddrTypeIPv4 || decoded.Addr != addr {
		t.Errorf("FlowOpen mismatch: %+v", decoded)
	}

	// Ack
	ackBytes := EncodeFlowOpenAck(flowID, StatusSuccess)
	decodedAck, err := DecodeFlowOpenAck(ackBytes)
	if err != nil {
		t.Fatalf("DecodeFlowOpenAck failed: %v", err)
	}
	if decodedAck.FlowID != flowID || decodedAck.Status != StatusSuccess {
		t.Errorf("FlowOpenAck mismatch: %+v", decodedAck)
	}
}

func TestPathChallengeCodec(t *testing.T) {
	pathID := uint16(3)
	var nonce [8]byte
	copy(nonce[:], []byte("CHAL1234"))

	encoded := EncodePathChallenge(pathID, nonce)
	decoded, err := DecodePathChallenge(encoded)
	if err != nil {
		t.Fatalf("DecodePathChallenge failed: %v", err)
	}

	if decoded.PathID != pathID {
		t.Errorf("PathID mismatch: got %d, want %d", decoded.PathID, pathID)
	}
	if decoded.Nonce != nonce {
		t.Errorf("Nonce mismatch: got %s, want %s", string(decoded.Nonce[:]), string(nonce[:]))
	}
}

func FuzzDecodeHeader(f *testing.F) {
	f.Add([]byte("DFTP\x03\x01\x00\x00\x00\x20\x00\x00\x00\x00\x00\x01\x00\x00\x00\x00\x00\x00\x00\x05\x00\x00\x00\x04\x00\x01\x00\x00test"))
	f.Add([]byte("garbagestringthatisnotavalidheaderatall"))
	f.Add([]byte(""))

	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = DecodeHeader(data)
		_, _ = DecodeFrame(data)
		_, _ = DecodeStreamFrame(data)
		_, _ = DecodeStreamAck(data)
		_, _ = DecodeFlowOpen(data)
	})
}

