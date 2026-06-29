package tlsfingerprint

import (
	"reflect"
	"strings"
	"testing"

	utls "github.com/refraction-networking/utls"
)

func TestBuiltInDefaultProfileMatchesLocalClaudeCodeCapture(t *testing.T) {
	if got := buildDefaultJA3Raw(); got != BuiltInDefaultJA3Raw {
		t.Fatalf("built-in JA3 raw mismatch:\n got: %s\nwant: %s", got, BuiltInDefaultJA3Raw)
	}

	wantExtensions := []uint16{0, 23, 65281, 10, 11, 35, 16, 5, 13, 18, 51, 45, 43, 21}
	if !reflect.DeepEqual(defaultExtensionOrder, wantExtensions) {
		t.Fatalf("default extension order = %v, want %v", defaultExtensionOrder, wantExtensions)
	}

	spec := buildClientHelloSpecFromProfile(nil)
	gotExtensions := extensionIDs(spec.Extensions)
	if !reflect.DeepEqual(gotExtensions, wantExtensions) {
		t.Fatalf("spec extension order = %v, want %v", gotExtensions, wantExtensions)
	}

	padding, ok := spec.Extensions[len(spec.Extensions)-1].(*utls.UtlsPaddingExtension)
	if !ok {
		t.Fatalf("last extension = %T, want *utls.UtlsPaddingExtension", spec.Extensions[len(spec.Extensions)-1])
	}
	padding.Update(300)
	if !padding.WillPad || padding.PaddingLen == 0 {
		t.Fatalf("padding extension did not apply BoringSSL padding style: willPad=%v len=%d", padding.WillPad, padding.PaddingLen)
	}

	if got := BuiltInDefaultProfile().Name; got != BuiltInDefaultProfileName {
		t.Fatalf("built-in profile name = %q, want %q", got, BuiltInDefaultProfileName)
	}
}

func buildDefaultJA3Raw() string {
	return strings.Join([]string{
		"771",
		joinUint16s(defaultCipherSuites),
		joinUint16s(defaultExtensionOrder),
		joinCurves(defaultCurves),
		joinUint16s(defaultPointFormats),
	}, ",")
}

func joinUint16s(vals []uint16) string {
	parts := make([]string, len(vals))
	for i, v := range vals {
		parts[i] = uint16ToDecimalString(v)
	}
	return strings.Join(parts, "-")
}

func joinCurves(vals []utls.CurveID) string {
	parts := make([]string, len(vals))
	for i, v := range vals {
		parts[i] = uint16ToDecimalString(uint16(v))
	}
	return strings.Join(parts, "-")
}

func uint16ToDecimalString(v uint16) string {
	if v == 0 {
		return "0"
	}
	var buf [5]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

func extensionIDs(exts []utls.TLSExtension) []uint16 {
	out := make([]uint16, 0, len(exts))
	for _, ext := range exts {
		switch ext.(type) {
		case *utls.SNIExtension:
			out = append(out, 0)
		case *utls.StatusRequestExtension:
			out = append(out, 5)
		case *utls.SupportedCurvesExtension:
			out = append(out, 10)
		case *utls.SupportedPointsExtension:
			out = append(out, 11)
		case *utls.SignatureAlgorithmsExtension:
			out = append(out, 13)
		case *utls.ALPNExtension:
			out = append(out, 16)
		case *utls.SCTExtension:
			out = append(out, 18)
		case *utls.UtlsPaddingExtension:
			out = append(out, 21)
		case *utls.ExtendedMasterSecretExtension:
			out = append(out, 23)
		case *utls.SessionTicketExtension:
			out = append(out, 35)
		case *utls.SupportedVersionsExtension:
			out = append(out, 43)
		case *utls.PSKKeyExchangeModesExtension:
			out = append(out, 45)
		case *utls.KeyShareExtension:
			out = append(out, 51)
		case *utls.RenegotiationInfoExtension:
			out = append(out, 65281)
		default:
			tlsExt, ok := ext.(*utls.GenericExtension)
			if !ok {
				out = append(out, 0xffff)
				continue
			}
			out = append(out, tlsExt.Id)
		}
	}
	return out
}
