//go:build linux && cgo

package keycard

/*
#cgo LDFLAGS: -ldl
#include <dlfcn.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

// The PC/SC types, mirrored rather than included.
//
// Including <PCSC/winscard.h> would mean the development headers at build time
// AND, through cgo's pkg-config directive, -lpcsclite at link time — which is
// the hard dependency this file exists to avoid. The upstream header splits
// exactly two ways, so mirroring it is two cases rather than a guess:
// wintypes.h uses int32/uint32 under __APPLE__ and long/unsigned long
// everywhere else. Measured on Linux: LONG 8, DWORD 8, SCARD_IO_REQUEST 16.
#ifdef __APPLE__
typedef int32_t  pcsc_long;
typedef uint32_t pcsc_dword;
#else
typedef long          pcsc_long;
typedef unsigned long pcsc_dword;
#endif

// SCARD_IO_REQUEST is unsigned long on both, being declared outside that
// #ifdef in pcsclite.h.
typedef struct {
	unsigned long dwProtocol;
	unsigned long cbPciLength;
} pcsc_io_request;

typedef pcsc_long (*fn_establish)(pcsc_dword, const void *, const void *, pcsc_long *);
typedef pcsc_long (*fn_release)(pcsc_long);
typedef pcsc_long (*fn_list)(pcsc_long, const char *, char *, pcsc_dword *);
typedef pcsc_long (*fn_connect)(pcsc_long, const char *, pcsc_dword, pcsc_dword, pcsc_long *, pcsc_dword *);
typedef pcsc_long (*fn_disconnect)(pcsc_long, pcsc_dword);
typedef pcsc_long (*fn_transmit)(pcsc_long, const pcsc_io_request *, const uint8_t *, pcsc_dword,
                                 pcsc_io_request *, uint8_t *, pcsc_dword *);

static void *pcsc_handle = NULL;
static fn_establish  p_establish;
static fn_release    p_release;
static fn_list       p_list;
static fn_connect    p_connect;
static fn_disconnect p_disconnect;
static fn_transmit   p_transmit;

// pcsc_load resolves the library on first use. 0 on success, 1 when the
// library is absent, 2 when it is present but missing a symbol.
static int pcsc_load(void) {
	if (pcsc_handle != NULL) return 0;
	// The soname, not the -dev symlink: libpcsclite.so exists only where the
	// development package is installed, which is not where this runs.
	pcsc_handle = dlopen("libpcsclite.so.1", RTLD_LAZY | RTLD_LOCAL);
	if (pcsc_handle == NULL) return 1;

	p_establish  = (fn_establish)dlsym(pcsc_handle, "SCardEstablishContext");
	p_release    = (fn_release)dlsym(pcsc_handle, "SCardReleaseContext");
	p_list       = (fn_list)dlsym(pcsc_handle, "SCardListReaders");
	p_connect    = (fn_connect)dlsym(pcsc_handle, "SCardConnect");
	p_disconnect = (fn_disconnect)dlsym(pcsc_handle, "SCardDisconnect");
	p_transmit   = (fn_transmit)dlsym(pcsc_handle, "SCardTransmit");
	if (!p_establish || !p_release || !p_list || !p_connect || !p_disconnect || !p_transmit) {
		dlclose(pcsc_handle);
		pcsc_handle = NULL;
		return 2;
	}
	return 0;
}

// 0x0002 is SCARD_SCOPE_SYSTEM.
static pcsc_long pcsc_establish(pcsc_long *ctx) {
	return p_establish(0x0002, NULL, NULL, ctx);
}
static pcsc_long pcsc_release(pcsc_long ctx)            { return p_release(ctx); }
static pcsc_long pcsc_list(pcsc_long ctx, char *buf, pcsc_dword *n) {
	return p_list(ctx, NULL, buf, n);
}
static pcsc_long pcsc_connect(pcsc_long ctx, const char *reader, pcsc_long *card, pcsc_dword *proto) {
	// Exclusive, and T=0 or T=1 — whichever the card offers.
	return p_connect(ctx, reader, 0x0001, 0x0001 | 0x0002, card, proto);
}
static pcsc_long pcsc_disconnect(pcsc_long card)        { return p_disconnect(card, 0x0001); }

static pcsc_long pcsc_transmit(pcsc_long card, pcsc_dword proto,
                               const uint8_t *in, pcsc_dword inlen,
                               uint8_t *out, pcsc_dword *outlen) {
	// The PCI built here rather than taken from the library's g_rgSCardT?Pci
	// globals: they are data symbols, and dlsym on data is one more thing to
	// get wrong for a struct the caller is allowed to supply. pcsclite reads
	// the two fields and nothing else.
	pcsc_io_request pci;
	pci.dwProtocol = proto;
	pci.cbPciLength = sizeof(pcsc_io_request);
	return p_transmit(card, &pci, in, inlen, NULL, out, outlen);
}
*/
import "C"

import (
	"errors"
	"fmt"
	"unsafe"
)

// Reaching a smartcard reader without depending on one being installable.
//
// libpcsclite is a CLIENT for a system daemon, which is what makes it different
// from every other library here. Two consequences decided this design:
//
// It cannot be bundled. A copy shipped beside the binary must match the host's
// pcscd, and when it does not the failure is "Service was stopped" — which
// reads as a broken card. Measured 2026-08-27: a bundled build could not see a
// reader that the same binary found immediately through the host's library.
//
// It cannot be a hard link either. Linking it puts libpcsclite.so.1 in
// DT_NEEDED, so a machine without it cannot start the binary AT ALL — including
// `shrooms daemon`, which never touches a card. That trades an optional feature
// for a node that will not boot.
//
// So it is opened at first use. One binary everywhere, card support wherever
// pcsc-lite is installed, and a headless server neither needs it nor notices.

// pcscMissing says what to install, because that is the only useful thing to
// say to somebody holding a reader and a card.
var pcscMissing = errors.New("no PC/SC library on this machine, which is how a " +
	"smartcard reader is reached.\n\n" +
	"Install it:\n" +
	"    sudo apt install pcscd libpcsclite1     # Debian, Ubuntu\n" +
	"    sudo dnf install pcsc-lite              # Fedora, RHEL\n" +
	"    sudo pacman -S pcsclite                 # Arch\n\n" +
	"Then plug the reader in. pcscd starts on demand — there is no service to enable.\n\n" +
	"Or use a phone, which reaches the same card over NFC")

func loadPCSC() error {
	switch C.pcsc_load() {
	case 0:
		return nil
	case 1:
		return pcscMissing
	default:
		return errors.New("this machine's libpcsclite is missing functions this " +
			"needs, which means it is a different library under the same name. " +
			"Reinstall pcsc-lite from your distribution")
	}
}

// pcscError turns a status word into something a person can act on. The codes
// that matter here are the ones a reader and a card produce day to day.
func pcscError(rv C.pcsc_long, what string) error {
	switch uint32(rv) {
	case 0x8010001D:
		return errors.New("the PC/SC service is not running, and could not be " +
			"started. Plug the reader in, or start it with `sudo systemctl start pcscd`")
	case 0x8010002E:
		return errors.New("no smartcard reader found — is one plugged in?")
	case 0x8010000C:
		return errors.New("no card on the reader")
	case 0x8010000F:
		return errors.New("the card is in use by something else — close any other " +
			"program talking to it")
	}
	return fmt.Errorf("%s: pcsc error 0x%08x", what, uint32(rv))
}

// pcscContext is an established PC/SC context.
type pcscContext struct{ ctx C.pcsc_long }

func pcscEstablish() (*pcscContext, error) {
	if err := loadPCSC(); err != nil {
		return nil, err
	}
	var ctx C.pcsc_long
	if rv := C.pcsc_establish(&ctx); rv != 0 {
		return nil, pcscError(rv, "could not reach the PC/SC service")
	}
	return &pcscContext{ctx: ctx}, nil
}

func (c *pcscContext) release() { C.pcsc_release(c.ctx) }

// readers lists attached readers. The list comes back as a run of C strings
// ending in an empty one.
func (c *pcscContext) readers() ([]string, error) {
	var n C.pcsc_dword
	if rv := C.pcsc_list(c.ctx, nil, &n); rv != 0 {
		if uint32(rv) == 0x8010002E { // no reader, which is not an error to ask about
			return nil, nil
		}
		return nil, pcscError(rv, "could not list readers")
	}
	if n == 0 {
		return nil, nil
	}
	buf := C.malloc(C.size_t(n))
	if buf == nil {
		return nil, errors.New("out of memory listing readers")
	}
	defer C.free(buf)
	if rv := C.pcsc_list(c.ctx, (*C.char)(buf), &n); rv != 0 {
		return nil, pcscError(rv, "could not list readers")
	}

	var out []string
	raw := C.GoBytes(buf, C.int(n))
	for _, part := range splitNul(raw) {
		out = append(out, part)
	}
	return out, nil
}

// splitNul reads the multi-string PC/SC returns: NUL-separated, NUL-terminated.
func splitNul(b []byte) []string {
	var out []string
	start := 0
	for i, c := range b {
		if c != 0 {
			continue
		}
		if i > start {
			out = append(out, string(b[start:i]))
		}
		start = i + 1
	}
	return out
}

// pcscCard is a connected card.
type pcscCard struct {
	card  C.pcsc_long
	proto C.pcsc_dword
}

func (c *pcscContext) connect(reader string) (*pcscCard, error) {
	name := C.CString(reader)
	defer C.free(unsafe.Pointer(name))

	var card C.pcsc_long
	var proto C.pcsc_dword
	if rv := C.pcsc_connect(c.ctx, name, &card, &proto); rv != 0 {
		return nil, pcscError(rv, "could not connect to the card")
	}
	return &pcscCard{card: card, proto: proto}, nil
}

func (c *pcscCard) disconnect() { C.pcsc_disconnect(c.card) }

// transmit sends one APDU and returns the response.
func (c *pcscCard) transmit(apdu []byte) ([]byte, error) {
	if len(apdu) == 0 {
		return nil, errors.New("empty APDU")
	}
	// 258 is the largest a short APDU answers with (256 + SW1 SW2); extended
	// length answers can be larger, so this is generous rather than exact.
	out := make([]byte, 4096)
	n := C.pcsc_dword(len(out))
	rv := C.pcsc_transmit(c.card, c.proto,
		(*C.uint8_t)(unsafe.Pointer(&apdu[0])), C.pcsc_dword(len(apdu)),
		(*C.uint8_t)(unsafe.Pointer(&out[0])), &n)
	if rv != 0 {
		return nil, pcscError(rv, "the card did not answer")
	}
	return out[:int(n)], nil
}
