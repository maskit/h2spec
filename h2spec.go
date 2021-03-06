package h2spec

import (
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
	"io"
	"math"
	"net"
	"os"
	"strings"
	"syscall"
	"time"
)

var TIMEOUT = errors.New("Timeout")

type RunMode int

const (
	ModeAll       RunMode = 0
	ModeGroupOnly RunMode = 1
	ModeSkip      RunMode = 2
)

type Context struct {
	Port      int
	Host      string
	Strict    bool
	Tls       bool
	TlsConfig *tls.Config
	Sections  map[string]bool
	Timeout   time.Duration
}

func (ctx *Context) Authority() string {
	return fmt.Sprintf("%s:%d", ctx.Host, ctx.Port)
}

func (ctx *Context) GetRunMode(section string) RunMode {
	if ctx.Sections == nil {
		return ModeAll
	}

	val, ok := ctx.Sections[section]
	if !ok {
		return ModeSkip
	}
	if !val {
		return ModeGroupOnly
	}

	return ModeAll
}

type Test interface {
	Run(*Context, int)
}

type TestGroup struct {
	Section      string
	Name         string
	testGroups   []*TestGroup
	testCases    []*TestCase
	numTestCases int // the number of test cases under this group
	numSkipped   int // the number of skipped test cases under this group
	numFailed    int // the number of failed test cases under this group
}

func (tg *TestGroup) Run(ctx *Context, level int) bool {
	pass := true
	runMode := ctx.GetRunMode(tg.Section)

	if runMode != ModeSkip {
		tg.PrintHeader(level)
	}

	if runMode == ModeAll {
		for _, testCase := range tg.testCases {
			switch testCase.Run(ctx, level+1) {
			case Failed:
				pass = false
				tg.numFailed += 1
			case Skipped:
				tg.numSkipped += 1
			}
		}
		tg.PrintFooter(level)
	} else {
		tg.numSkipped += tg.numTestCases
	}

	for _, testGroup := range tg.testGroups {
		if !testGroup.Run(ctx, level+1) {
			pass = false
		}
	}

	return pass
}

// PrintFailedTestCase prints failed TestCase results under this
// TestGroup.
func (tg *TestGroup) PrintFailedTestCase(level int) {
	if tg.CountFailed() == 0 {
		return
	}

	tg.PrintHeader(level)

	numTestCaseFailed := 0
	for _, tc := range tg.testCases {
		if tc.failed {
			tc.PrintFail(tc.expected, tc.actual, level+1)
			numTestCaseFailed += 1
		}
	}

	if numTestCaseFailed > 0 {
		fmt.Println("")
	}

	for _, testGroup := range tg.testGroups {
		testGroup.PrintFailedTestCase(level + 1)
	}
}

func (tg *TestGroup) AddTestCase(testCase *TestCase) {
	tg.testCases = append(tg.testCases, testCase)
	tg.numTestCases += 1
}

func (tg *TestGroup) AddTestGroup(testGroup *TestGroup) {
	tg.testGroups = append(tg.testGroups, testGroup)
}

func (tg *TestGroup) CountTestCases() int {
	num := tg.numTestCases
	for _, testGroup := range tg.testGroups {
		num += testGroup.CountTestCases()
	}

	return num
}

func (tg *TestGroup) CountSkipped() int {
	num := tg.numSkipped
	for _, testGroup := range tg.testGroups {
		num += testGroup.CountSkipped()
	}

	return num
}

func (tg *TestGroup) CountFailed() int {
	num := tg.numFailed
	for _, testGroup := range tg.testGroups {
		num += testGroup.CountFailed()
	}

	return num
}

func (tg *TestGroup) PrintHeader(level int) {
	indent := strings.Repeat("  ", level)
	fmt.Printf("%s%s. %s\n", indent, tg.Section, tg.Name)
}

func (tg *TestGroup) PrintFooter(level int) {
	if len(tg.testCases) == 0 {
		return
	}
	fmt.Println("")
}

type TestCase struct {
	Desc     string
	Spec     string
	handler  func(*Context) (bool, []Result, Result)
	failed   bool     // true if test failed
	expected []Result // expected result
	actual   Result   // actual result
}

type TestResult int

// TestResult indicates the result of test case
const (
	Failed TestResult = iota
	Skipped
	Passed
)

func (tc *TestCase) Run(ctx *Context, level int) TestResult {
	tc.PrintEphemeralDesc(level)

	pass, expected, actual := tc.handler(ctx)
	_, ok := actual.(*ResultSkipped)
	if ok {
		tc.PrintSkipped(actual, level)
		return Skipped
	}

	// keep expected and actual so that we can report the failed
	// test cases in summary.
	tc.expected = expected
	tc.actual = actual

	if pass {
		tc.PrintPass(level)
		return Passed
	} else {
		tc.failed = true
		tc.PrintFail(expected, actual, level)
		return Failed
	}
}

func (tc *TestCase) HandleFunc(handler func(*Context) (bool, []Result, Result)) {
	tc.handler = handler
}

func (tc *TestCase) PrintEphemeralDesc(level int) {
	indent := strings.Repeat("  ", level)
	fmt.Printf("%s  \x1b[90m%s\x1b[0m", indent, tc.Desc)
}

func (tc *TestCase) PrintPass(level int) {
	mark := "✓"
	indent := strings.Repeat("  ", level)
	fmt.Printf("\r%s\x1b[32m%s\x1b[0m \x1b[90m%s\x1b[0m\n", indent, mark, tc.Desc)
}

func (tc *TestCase) PrintFail(expected []Result, actual Result, level int) {
	mark := "×"
	indent := strings.Repeat("  ", level)

	fmt.Printf("\r\x1b[31m")
	fmt.Printf("%s%s %s\n", indent, mark, tc.Desc)
	fmt.Printf("%s  - %s\n", indent, tc.Spec)
	fmt.Printf("\x1b[32m")
	for i, exp := range expected {
		var lavel string
		if i == 0 {
			lavel = "Expected:"
		} else {
			lavel = strings.Repeat(" ", 9)
		}
		fmt.Printf("%s    %s %s\n", indent, lavel, exp)
	}
	fmt.Printf("\x1b[33m")
	fmt.Printf("%s      Actual: %s\n", indent, actual)
	fmt.Printf("\x1b[0m")
}

func (tc *TestCase) PrintSkipped(actual Result, level int) {
	mark := " "
	indent := strings.Repeat("  ", level)

	fmt.Printf("\r\x1b[36m")
	fmt.Printf("%s%s %s\n", indent, mark, tc.Desc)
	fmt.Printf("%s  - %s\n", indent, actual)
	fmt.Printf("\x1b[0m")
}

func NewTestGroup(section, name string) *TestGroup {
	return &TestGroup{
		Section: section,
		Name:    name,
	}
}

func NewTestCase(desc, spec string, handler func(*Context) (bool, []Result, Result)) *TestCase {
	return &TestCase{
		Desc:    desc,
		Spec:    spec,
		handler: handler,
	}
}

var LengthDefault uint32 = math.MaxUint32
var FlagDefault http2.Flags = math.MaxUint8
var ErrCodeDefault http2.ErrCode = math.MaxUint8

type Result interface {
	String() string
}

type ResultFrame struct {
	Length  uint32
	Type    http2.FrameType
	Flags   http2.Flags
	ErrCode http2.ErrCode
}

func (rf *ResultFrame) String() string {
	parts := []string{}

	if rf.Length != LengthDefault {
		parts = append(parts, fmt.Sprintf("Length: %d", rf.Length))
	}
	if rf.Flags != FlagDefault {
		parts = append(parts, fmt.Sprintf("Flags: %d", rf.Flags))
	}
	if rf.ErrCode != ErrCodeDefault {
		parts = append(parts, fmt.Sprintf("ErrorCode: %s", rf.ErrCode.String()))
	}

	res := fmt.Sprintf("%s frame", rf.Type.String())
	if len(parts) > 0 {
		res += fmt.Sprintf(" (%s)", strings.Join(parts, ", "))
	}

	return res
}

type ResultConnectionClose struct{}

func (rcc *ResultConnectionClose) String() string {
	return "Connection close"
}

type ResultStreamClose struct{}

func (rsc *ResultStreamClose) String() string {
	return "Stream close"
}

type ResultTestTimeout struct{}

func (ttr *ResultTestTimeout) String() string {
	return "Test timeout"
}

type ResultSkipped struct {
	Reason string
}

func (rs *ResultSkipped) String() string {
	return "Skipped: " + rs.Reason
}

type ResultError struct {
	Error error
}

func (re *ResultError) String() string {
	return fmt.Sprintf("Error: %s", re.Error)
}

type TcpConn struct {
	conn   net.Conn
	dataCh chan []byte
	errCh  chan error
}

type Http2Conn struct {
	conn           net.Conn
	dataCh         chan http2.Frame
	errCh          chan error
	fr             *http2.Framer
	HpackEncoder   *hpack.Encoder
	HeaderWriteBuf bytes.Buffer
	Settings       map[http2.SettingID]uint32
}

// ReadFrame reads a complete HTTP/2 frame from underlying connection.
// This function blocks until a complete frame is received or timeout
// t is expired.  The returned http2.Frame must not be used after next
// ReadFrame call.
func (h2Conn *Http2Conn) ReadFrame(t time.Duration) (http2.Frame, error) {
	go func() {
		f, err := h2Conn.fr.ReadFrame()
		if err != nil {
			h2Conn.errCh <- err
			return
		}
		h2Conn.dataCh <- f
	}()

	select {
	case f := <-h2Conn.dataCh:
		return f, nil
	case err := <-h2Conn.errCh:
		return nil, err
	case <-time.After(t):
		return nil, TIMEOUT
	}
}

// EncodeHeader encodes header and returns encoded bytes.  h2Conn
// retains encoding context and next call of EncodeHeader will be
// performed using the same encoding context.
func (h2Conn *Http2Conn) EncodeHeader(header []hpack.HeaderField) []byte {
	h2Conn.HeaderWriteBuf.Reset()

	for _, hf := range header {
		_ = h2Conn.HpackEncoder.WriteField(hf)
	}

	dst := make([]byte, h2Conn.HeaderWriteBuf.Len())
	copy(dst, h2Conn.HeaderWriteBuf.Bytes())

	return dst
}

func connectTls(ctx *Context) (net.Conn, error) {
	if ctx.TlsConfig == nil {
		ctx.TlsConfig = new(tls.Config)
	}

	if ctx.TlsConfig.NextProtos == nil {
		ctx.TlsConfig.NextProtos = append(ctx.TlsConfig.NextProtos, "h2-14", "h2-15", "h2-16", "h2")
	}

	dialer := new(net.Dialer)
	dialer.Timeout = ctx.Timeout
	conn, err := tls.DialWithDialer(dialer, "tcp", ctx.Authority(), ctx.TlsConfig)
	if err != nil {
		return nil, err
	}

	cs := conn.ConnectionState()
	if !cs.NegotiatedProtocolIsMutual {
		return nil, fmt.Errorf("HTTP/2 protocol was not negotiated")
	}

	return conn, err
}

func CreateTcpConn(ctx *Context) *TcpConn {
	var conn net.Conn
	var err error

	if ctx.Tls {
		conn, err = connectTls(ctx)
	} else {
		conn, err = net.DialTimeout("tcp", ctx.Authority(), ctx.Timeout)
	}

	if err != nil {
		printError(fmt.Sprintf("Unable to connect to the target server (%v)", err))
		os.Exit(1)
	}

	dataCh := make(chan []byte)
	errCh := make(chan error, 1)

	tcpConn := &TcpConn{
		conn:   conn,
		dataCh: dataCh,
		errCh:  errCh,
	}

	go func() {
		for {
			buf := make([]byte, 512)
			_, err := conn.Read(buf)
			dataCh <- buf
			if err != nil {
				errCh <- err
				return
			}
		}
	}()

	return tcpConn
}

func CreateHttp2Conn(ctx *Context, sn bool) *Http2Conn {
	var conn net.Conn
	var err error

	if ctx.Tls {
		conn, err = connectTls(ctx)
	} else {
		conn, err = net.DialTimeout("tcp", ctx.Authority(), ctx.Timeout)
	}

	if err != nil {
		printError(fmt.Sprintf("Unable to connect to the target server (%v)", err))
		os.Exit(1)
	}

	fmt.Fprintf(conn, "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n")

	fr := http2.NewFramer(conn, conn)
	settings := map[http2.SettingID]uint32{}

	if sn {
		doneCh := make(chan bool, 1)
		errCh := make(chan error, 1)
		fr.WriteSettings()

		go func() {
			local := false
			remote := false

			for {
				f, err := fr.ReadFrame()
				if err != nil {
					errCh <- err
					return
				}

				switch f := f.(type) {
				case *http2.SettingsFrame:
					if f.IsAck() {
						local = true
					} else {
						f.ForeachSetting(func(setting http2.Setting) error {
							settings[setting.ID] = setting.Val
							return nil
						})
						fr.WriteSettingsAck()
						remote = true
					}
				}

				if local && remote {
					doneCh <- true
					return
				}
			}
		}()

		select {
		case <-doneCh:
			// Nothing to do.
		case <-errCh:
			printError("HTTP/2 settings negotiation failed")
			os.Exit(1)
		case <-time.After(ctx.Timeout):
			printError("HTTP/2 settings negotiation timeout")
			os.Exit(1)
		}
	}

	fr.AllowIllegalWrites = true
	dataCh := make(chan http2.Frame)
	errCh := make(chan error, 1)

	http2Conn := &Http2Conn{
		conn:     conn,
		fr:       fr,
		dataCh:   dataCh,
		errCh:    errCh,
		Settings: settings,
	}

	http2Conn.HpackEncoder = hpack.NewEncoder(&http2Conn.HeaderWriteBuf)

	return http2Conn
}

//func CreateHttp2ConnWithSettings(ctx *Context, settings ...http2.Setting) *Http2Conn {
//	http2Conn := CreateHttp2Conn(ctx, false)
//
//	doneCh := make(chan bool, 1)
//	errCh := make(chan error, 1)
//	http2Conn.fr.WriteSettings(settings)
//
//	go func() {
//		local := false
//		remote := false
//
//		for {
//			f, err := http2Conn.fr.ReadFrame()
//			if err != nil {
//				errCh <- err
//				return
//			}
//
//			switch f := f.(type) {
//			case *http2.SettingsFrame:
//				if f.IsAck() {
//					local = true
//				} else {
//					f.ForeachSetting(func(setting http2.Setting) error {
//						settings[setting.ID] = setting.Val
//						return nil
//					})
//					http2Conn.fr.WriteSettingsAck()
//					remote = true
//				}
//			}
//
//			if local && remote {
//				doneCh <- true
//				return
//			}
//		}
//	}()
//
//	select {
//	case <-doneCh:
//		// Nothing to do.
//	case <-errCh:
//		fmt.Println("HTTP/2 settings negotiation failed")
//		os.Exit(1)
//	case <-time.After(ctx.Timeout):
//		fmt.Println("HTTP/2 settings negotiation timeout")
//		os.Exit(1)
//	}
//
//	return http2Conn
//}

func TestConnectionError(ctx *Context, http2Conn *Http2Conn, codes []http2.ErrCode) (pass bool, expected []Result, actual Result) {
	for _, code := range codes {
		expected = append(expected, &ResultFrame{LengthDefault, http2.FrameGoAway, FlagDefault, code})
	}
	expected = append(expected, &ResultConnectionClose{})

loop:
	for {
		f, err := http2Conn.ReadFrame(ctx.Timeout)
		if err != nil {
			opErr, ok := err.(*net.OpError)
			if err == io.EOF || (ok && opErr.Err == syscall.ECONNRESET) {
				rf, ok := actual.(*ResultFrame)
				if actual == nil || (ok && rf.Type != http2.FrameGoAway) {
					actual = &ResultConnectionClose{}
					pass = true
				}
			} else if err == TIMEOUT {
				if actual == nil {
					actual = &ResultTestTimeout{}
				}
			} else {
				actual = &ResultError{err}
			}
			break loop
		}

		switch f := f.(type) {
		case *http2.GoAwayFrame:
			actual = CreateResultFrame(f)
			if TestErrorCode(f.ErrCode, codes) {
				pass = true
				break loop
			}
		default:
			actual = CreateResultFrame(f)
		}
	}

	return pass, expected, actual
}

func TestStreamError(ctx *Context, http2Conn *Http2Conn, codes []http2.ErrCode) (pass bool, expected []Result, actual Result) {
	pass = false

	for _, code := range codes {
		expected = append(expected, &ResultFrame{LengthDefault, http2.FrameGoAway, FlagDefault, code})
		expected = append(expected, &ResultFrame{LengthDefault, http2.FrameRSTStream, FlagDefault, code})
	}
	expected = append(expected, &ResultConnectionClose{})

loop:
	for {
		f, err := http2Conn.ReadFrame(ctx.Timeout)
		if err != nil {
			opErr, ok := err.(*net.OpError)
			if err == io.EOF || (ok && opErr.Err == syscall.ECONNRESET) {
				rf, ok := actual.(*ResultFrame)
				if actual == nil || (ok && rf.Type != http2.FrameGoAway) {
					actual = &ResultConnectionClose{}
					pass = true
				}
			} else if err == TIMEOUT {
				if actual == nil {
					actual = &ResultTestTimeout{}
				}
			} else {
				actual = &ResultError{err}
			}
			break loop
		}

		switch f := f.(type) {
		case *http2.GoAwayFrame:
			actual = CreateResultFrame(f)
			if TestErrorCode(f.ErrCode, codes) {
				pass = true
				break loop
			}
		case *http2.RSTStreamFrame:
			actual = CreateResultFrame(f)
			if TestErrorCode(f.ErrCode, codes) {
				pass = true
				break loop
			}
		default:
			actual = CreateResultFrame(f)
		}
	}

	return pass, expected, actual
}

func TestStreamClose(ctx *Context, http2Conn *Http2Conn) (pass bool, expected []Result, actual Result) {
	pass = false
	expected = append(expected, &ResultStreamClose{})

loop:
	for {
		f, err := http2Conn.ReadFrame(ctx.Timeout)
		if err != nil {
			opErr, ok := err.(*net.OpError)
			if err == io.EOF || (ok && opErr.Err == syscall.ECONNRESET) {
				rf, ok := actual.(*ResultFrame)
				if actual == nil || (ok && rf.Type != http2.FrameGoAway) {
					actual = &ResultConnectionClose{}
					pass = true
				}
			} else if err == TIMEOUT {
				if actual == nil {
					actual = &ResultTestTimeout{}
				}
			} else {
				actual = &ResultError{err}
			}
			break loop
		}

		switch f := f.(type) {
		case *http2.DataFrame:
			if f.StreamEnded() {
				pass = true
				actual = &ResultStreamClose{}
				break loop
			} else {
				actual = CreateResultFrame(f)
			}
		case *http2.HeadersFrame:
			if f.StreamEnded() {
				pass = true
				actual = &ResultStreamClose{}
				break loop
			} else {
				actual = CreateResultFrame(f)
			}
		default:
			actual = CreateResultFrame(f)
		}
	}

	return pass, expected, actual
}

func TestErrorCode(code http2.ErrCode, expected []http2.ErrCode) bool {
	for _, exp := range expected {
		if code == exp {
			return true
		}
	}
	return false
}

func CreateResultFrame(f http2.Frame) (rf *ResultFrame) {
	rf = &ResultFrame{
		Type:   f.Header().Type,
		Flags:  f.Header().Flags,
		Length: f.Header().Length,
	}

	switch f := f.(type) {
	case *http2.GoAwayFrame:
		rf.ErrCode = f.ErrCode
	case *http2.RSTStreamFrame:
		rf.ErrCode = f.ErrCode
	default:
		rf.ErrCode = ErrCodeDefault
	}

	return rf
}

func commonHeaderFields(ctx *Context) []hpack.HeaderField {
	var scheme, authority string
	defaultPort := false

	if ctx.Tls {
		scheme = "https"

		if ctx.Port == 443 {
			defaultPort = true
		}
	} else {
		scheme = "http"

		if ctx.Port == 80 {
			defaultPort = true
		}
	}

	if defaultPort {
		authority = ctx.Host
	} else {
		authority = ctx.Authority()
	}

	return []hpack.HeaderField{
		pair(":method", "GET"),
		pair(":scheme", scheme),
		pair(":path", "/"),
		pair(":authority", authority),
	}
}

func dummyData(num int) string {
	var data string
	for i := 0; i < num; i++ {
		data += "x"
	}
	return data
}

func pair(name, value string) hpack.HeaderField {
	return hpack.HeaderField{Name: name, Value: value}
}

func printError(err string) {
	fmt.Printf("\n\n\x1b[31m")
	fmt.Printf("ERROR: %s", err)
	fmt.Printf("\x1b[0m\n")
}

// printSummary prints out the test summary of all tests performed.
func printSummary(groups []*TestGroup) {
	numTestCases := 0
	numSkipped := 0
	numFailed := 0

	for _, tg := range groups {
		numTestCases += tg.CountTestCases()
		numSkipped += tg.CountSkipped()
		numFailed += tg.CountFailed()
	}

	numPassed := numTestCases - numSkipped - numFailed

	fmt.Printf("\x1b[90m")
	fmt.Printf("%v tests, %v passed, %v skipped, %v failed\n", numTestCases, numPassed, numSkipped, numFailed)
	fmt.Printf("\x1b[0m")

	if numFailed == 0 {
		fmt.Printf("\x1b[90m")
		fmt.Printf("All tests passed\n")
		fmt.Printf("\x1b[0m")
	} else {
		fmt.Println("")
		fmt.Printf("\x1b[31m")
		fmt.Println("===============================================================================")
		fmt.Println("Failed tests")
		fmt.Println("===============================================================================")
		fmt.Printf("\x1b[0m")
		fmt.Println("")

		if numFailed > 0 {
			for _, tg := range groups {
				tg.PrintFailedTestCase(1)
			}
		}
	}
}

func Run(ctx *Context) bool {
	pass := true

	groups := []*TestGroup{
		Http2ConnectionPrefaceTestGroup(ctx),
		FrameSizeTestGroup(ctx),
		HeaderCompressionAndDecompressionTestGroup(ctx),
		StreamStatesTestGroup(ctx),
		StreamPriorityTestGroup(ctx),
		ErrorHandlingTestGroup(ctx),
		ExtendingHttp2TestGroup(ctx),
		DataTestGroup(ctx),
		HeadersTestGroup(ctx),
		PriorityTestGroup(ctx),
		RstStreamTestGroup(ctx),
		SettingsTestGroup(ctx),
		PingTestGroup(ctx),
		GoawayTestGroup(ctx),
		WindowUpdateTestGroup(ctx),
		ContinuationTestGroup(ctx),
		HttpRequestResponseExchangeTestGroup(ctx),
		ServerPushTestGroup(ctx),
	}

	for _, group := range groups {
		if !group.Run(ctx, 1) {
			pass = false
		}
	}

	printSummary(groups)

	return pass
}
