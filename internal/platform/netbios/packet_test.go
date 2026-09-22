package netbios

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// encodedStar — имя «*» в first-level-кодировке, как в запросе и в RR_NAME ответа.
func encodedStar() []byte {
	b := []byte{encodedNameBytes, 'C', 'K'}
	b = append(b, bytes.Repeat([]byte{'A'}, encodedNameBytes-2)...)
	return append(b, 0)
}

// nameTable собирает RDATA ответа: NUM_NAMES, таблица имён, блок статистики.
func nameTable(names []Name, withStats bool) []byte {
	b := []byte{byte(len(names))}
	for _, n := range names {
		field := make([]byte, netbiosNameLen)
		copy(field, n.Name)
		for i := len(n.Name); i < netbiosNameLen; i++ {
			field[i] = ' '
		}
		b = append(b, field...)
		b = append(b, n.Suffix)
		flags := n.Flags
		if n.Group {
			flags |= flagGroup
		}
		b = binary.BigEndian.AppendUint16(b, flags)
	}
	if withStats {
		b = append(b, make([]byte, 46)...) // MAC и счётчики — не читаются
	}
	return b
}

// respSpec — конструктор ответа NBSTAT для тестов, в том числе испорченного.
type respSpec struct {
	txID   uint16
	flags  uint16
	qd     int    // сколько вопросов повторить перед ответом
	an     uint16 // ANCOUNT
	rrName []byte // nil → encodedStar()
	rrType uint16
	rdata  []byte
	rdLen  int // < 0 → len(rdata)
}

func validSpec(txID uint16, names []Name) respSpec {
	return respSpec{
		txID: txID, flags: 0x8400, an: 1, rrType: typeNBSTAT,
		rdata: nameTable(names, true), rdLen: -1,
	}
}

func (s respSpec) bytes() []byte {
	b := binary.BigEndian.AppendUint16(nil, s.txID)
	b = binary.BigEndian.AppendUint16(b, s.flags)
	b = binary.BigEndian.AppendUint16(b, uint16(s.qd))
	b = binary.BigEndian.AppendUint16(b, s.an)
	b = append(b, 0, 0, 0, 0)
	for range s.qd {
		b = append(b, encodedStar()...)
		b = binary.BigEndian.AppendUint16(b, typeNBSTAT)
		b = binary.BigEndian.AppendUint16(b, classIN)
	}
	name := s.rrName
	if name == nil {
		name = encodedStar()
	}
	b = append(b, name...)
	b = binary.BigEndian.AppendUint16(b, s.rrType)
	b = binary.BigEndian.AppendUint16(b, classIN)
	b = append(b, 0, 0, 0, 0) // TTL
	rdLen := s.rdLen
	if rdLen < 0 {
		rdLen = len(s.rdata)
	}
	b = binary.BigEndian.AppendUint16(b, uint16(rdLen))
	return append(b, s.rdata...)
}

// sqlDevNames — таблица имён, которую SRV-D-SQLDEV-HQ вернул на боевой пробе
// (nmblookup -A 192.168.6.167, §106.1).
func sqlDevNames() []Name {
	return []Name{
		{Name: "SRV-D-SQLDEV-HQ", Suffix: 0x00, Flags: 0x0400},
		{Name: "VZ78", Suffix: 0x00, Group: true, Flags: 0x8400},
		{Name: "SRV-D-SQLDEV-HQ", Suffix: 0x20, Flags: 0x0400},
	}
}

func TestBuildNodeStatusRequest_Golden(t *testing.T) {
	t.Parallel()
	got := BuildNodeStatusRequest(0x8228)
	want := "8228" + "0000" + "0001" + "000000000000" +
		"20" + "434b" + strings.Repeat("41", 30) + "00" +
		"0021" + "0001"
	require.Len(t, got, NodeStatusRequestLen)
	assert.Equal(t, want, hex.EncodeToString(got))
}

func TestParseNodeStatusResponse(t *testing.T) {
	t.Parallel()
	const tx = 0x1234
	valid := validSpec(tx, sqlDevNames())

	with := func(mut func(*respSpec)) []byte {
		s := valid
		mut(&s)
		return s.bytes()
	}
	truncatedTable := with(func(s *respSpec) {
		s.rdata = nameTable(sqlDevNames()[:1], false)
		s.rdata[0] = 3 // NUM_NAMES врёт: в таблице одна запись
	})
	noAnswer := valid.bytes()
	binary.BigEndian.PutUint16(noAnswer[6:8], 0)

	tests := []struct {
		name    string
		pkt     []byte
		want    []Name
		wantErr error
	}{
		{name: "valid", pkt: valid.bytes(), want: sqlDevNames()},
		{name: "wrong_txid", pkt: with(func(s *respSpec) { s.txID = tx + 1 }), wantErr: ErrTransactionID},
		{name: "qr_bit_clear", pkt: with(func(s *respSpec) { s.flags = 0x0400 }), wantErr: ErrNotResponse},
		{name: "rcode_nonzero", pkt: with(func(s *respSpec) { s.flags = 0x8403 }), wantErr: ErrRCode},
		{name: "ancount_zero", pkt: noAnswer, wantErr: ErrNoAnswer},
		{name: "truncated_header", pkt: valid.bytes()[:8], wantErr: ErrShortPacket},
		{name: "truncated_table", pkt: truncatedTable, wantErr: ErrShortPacket},
		{name: "zero_names", pkt: with(func(s *respSpec) { s.rdata = []byte{0} }), want: []Name{}},
		{name: "empty_rdata", pkt: with(func(s *respSpec) { s.rdata = nil }), wantErr: ErrShortPacket},
		{name: "echoed_question", pkt: with(func(s *respSpec) { s.qd = 1 }), want: sqlDevNames()},
		{name: "compressed_name_pointer", pkt: with(func(s *respSpec) { s.rrName = []byte{0xC0, 0x0C} }), want: sqlDevNames()},
		{name: "rdlength_exceeds_packet", pkt: with(func(s *respSpec) { s.rdLen = len(s.rdata) + 10 }), wantErr: ErrShortPacket},
		{name: "wrong_rr_type", pkt: with(func(s *respSpec) { s.rrType = 0x0020 }), wantErr: ErrBadRecord},
		{name: "bad_label_byte", pkt: with(func(s *respSpec) { s.rrName = []byte{0x80} }), wantErr: ErrBadRecord},
		{name: "name_runs_past_end", pkt: append(valid.bytes()[:headerLen], 0x3F), wantErr: ErrShortPacket},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseNodeStatusResponse(tx, tt.pkt)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseNodeStatusResponse_TrimsNULPadding(t *testing.T) {
	t.Parallel()
	s := validSpec(7, []Name{{Name: "HOST", Suffix: 0x00}})
	copy(s.rdata[1+4:1+netbiosNameLen], bytes.Repeat([]byte{0}, netbiosNameLen-4))
	got, err := ParseNodeStatusResponse(7, s.bytes())
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "HOST", got[0].Name)
}

func TestWorkstationName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		names  []Name
		want   string
		wantOK bool
	}{
		{name: "production_table", names: sqlDevNames(), want: "SRV-D-SQLDEV-HQ", wantOK: true},
		{
			name:  "group_first_skipped",
			names: []Name{{Name: "VZ78", Group: true}, {Name: "SRV-D-1C"}, {Name: "SRV-D-1C", Suffix: 0x20}},
			want:  "SRV-D-1C", wantOK: true,
		},
		{name: "underscore_and_hyphen", names: []Name{{Name: "PC_78-01"}}, want: "PC_78-01", wantOK: true},
		{name: "oem_byte_skipped", names: []Name{{Name: "\x80BAD"}, {Name: "GOOD"}}, want: "GOOD", wantOK: true},
		{name: "space_and_quote_rejected", names: []Name{{Name: "A B"}, {Name: "A'B"}, {Name: "A.B"}}},
		{name: "longer_than_15_rejected", names: []Name{{Name: "ABCDEFGHIJKLMNOP"}}},
		{name: "only_file_server_suffix", names: []Name{{Name: "HOST", Suffix: 0x20}}},
		{name: "only_groups", names: []Name{{Name: "VZ78", Group: true}}},
		{name: "empty", names: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := WorkstationName(tt.names)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

// FuzzParseNodeStatusResponse: на любых байтах парсер не паникует, а
// разобранные имена укладываются в поле из 15 байт.
func FuzzParseNodeStatusResponse(f *testing.F) {
	valid := validSpec(0x1234, sqlDevNames())
	f.Add(uint16(0x1234), valid.bytes())
	echoed := valid
	echoed.qd = 2
	f.Add(uint16(0x1234), echoed.bytes())
	f.Add(uint16(0x1234), valid.bytes()[:40])
	f.Add(uint16(0), []byte{})
	f.Add(uint16(0x1234), BuildNodeStatusRequest(0x1234))
	f.Fuzz(func(t *testing.T, txID uint16, pkt []byte) {
		names, err := ParseNodeStatusResponse(txID, pkt)
		if err != nil {
			return
		}
		for _, n := range names {
			if len(n.Name) > netbiosNameLen {
				t.Fatalf("name longer than %d bytes: %q", netbiosNameLen, n.Name)
			}
		}
	})
}
