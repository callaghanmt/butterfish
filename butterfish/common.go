package butterfish

import (
	"fmt"
	"io"
	"log"
	"regexp"
	"strconv"
)

// See https://platform.openai.com/docs/models/overview
var MODEL_TO_NUM_TOKENS = map[string]int{
	"gpt-4":                  8192,
	"gpt-4-0314":             8192,
	"gpt-4-0613":             8192,
	"gpt-4-32k":              32768,
	"gpt-4-32k-0314":         32768,
	"gpt-4-32k-0613":         32768,
	"gpt-3.5-turbo":          4096,
	"gpt-3.5-turbo-0301":     4096,
	"gpt-3.5-turbo-16k":      16384,
	"gpt-3.5-turbo-16k-0613": 16384,
	"text-davinci-003":       2047,
	"text-davinci-002":       2047,
	"code-davinci-002":       8001,
	"code-davinci-001":       8001,
	"text-curie-001":         2049,
	"text-babbage-001":       2049,
	"text-ada-001":           2049,
	"davinci":                2049,
	"curie":                  2049,
	"babbage":                2049,
	"ada":                    2049,
	"code-cushman-002":       2048,
	"code-cushman-001":       2048,
}

// these token numbers come from
// https://github.com/pkoukk/tiktoken-go#counting-tokens-for-chat-api-calls
var MODEL_TO_TOKENS_PER_MESSAGE = map[string]int{
	"gpt-4":                  3,
	"gpt-4-0314":             3,
	"gpt-4-0613":             3,
	"gpt-4-32k":              3,
	"gpt-4-32k-0314":         3,
	"gpt-4-32k-0613":         3,
	"gpt-3.5-turbo":          4,
	"gpt-3.5-turbo-0301":     4,
	"gpt-3.5-turbo-16k":      4,
	"gpt-3.5-turbo-16k-0613": 4,
}

func NumTokensForModel(model string) int {
	numTokens, ok := MODEL_TO_NUM_TOKENS[model]
	if ok {
		return numTokens
	}

	log.Printf("WARNING: Unknown model %s, using default num tokens 2048", model)
	return 2048
}

func NumTokensPerMessageForModel(model string) int {
	numTokens, ok := MODEL_TO_TOKENS_PER_MESSAGE[model]
	if ok {
		return numTokens
	}

	log.Printf("WARNING: Unknown model %s, using default num tokens per message 4", model)
	return 4
}

// Data type for passing byte chunks from a wrapped command around
type byteMsg struct {
	Data []byte
}

type cursorPosition struct {
	Row    int
	Column int
}

func NewByteMsg(data []byte) *byteMsg {
	buf := make([]byte, len(data))
	copy(buf, data)
	return &byteMsg{
		Data: buf,
	}
}

// Given an io.Reader we write byte chunks to a channel
func readerToChannel(input io.Reader, c chan<- *byteMsg) {
	buf := make([]byte, 1024*16)

	// Loop indefinitely
	for {
		// Read from stream
		n, err := input.Read(buf)

		// Check for error
		if err != nil {
			if err != io.EOF {
				log.Printf("Error reading from file: %s\n", err)
			}
			break
		}

		if n >= 2 && buf[0] == '\x1b' && buf[1] == '[' && !ansiCsiPattern.Match(buf[:n]) {
			log.Printf("got escape sequence: %x", buf)
			// We have downstream code that assumes full ANSI sequences, so we validate
			// here, I've never seen this fire
			panic("Got incomplete escape sequence")
		}

		c <- NewByteMsg(buf[:n])
	}

	// Close the channel
	close(c)
}

// compile a regex that matches \x1b[%d;%dR
var cursorPosRegex = regexp.MustCompile(`\x1b\[(\d+);(\d+)R`)

// Search for an ANSI cursor position sequence, e.g. \x1b[4;14R, and return:
// - row
// - column
// - length of the sequence
// - whether the sequence was found
func parseCursorPos(data []byte) (int, int, bool) {
	matches := cursorPosRegex.FindSubmatch(data)
	if len(matches) != 3 {
		return -1, -1, false
	}
	row, err := strconv.Atoi(string(matches[1]))
	if err != nil {
		return -1, -1, false
	}
	col, err := strconv.Atoi(string(matches[2]))
	if err != nil {
		return -1, -1, false
	}
	return row, col, true
}

// Given an io.Reader we write byte chunks to a channel
// This is a modified version with a separate channel for cursor position
func readerToChannelWithPosition(input io.Reader, c chan<- *byteMsg, pos chan<- *cursorPosition) {
	buf := make([]byte, 1024*16)

	// Loop indefinitely
	for {
		// Read from stream
		n, err := input.Read(buf)

		// Check for error
		if err != nil {
			if err != io.EOF {
				log.Printf("Error reading from file: %s\n", err)
			}
			break
		}

		// if we find a cursor position, send it to the pos channel but leave it in
		row, col, found := parseCursorPos(buf[:n])
		if found {
			pos <- &cursorPosition{
				Row:    row,
				Column: col,
			}

			cleaned := cursorPosRegex.ReplaceAll(buf[:n], []byte{})
			copy(buf, cleaned)
			n = len(cleaned)
			if n == 0 {
				continue
			}
		}

		if n >= 2 && buf[0] == '\x1b' && buf[1] == '[' && !ansiCsiPattern.Match(buf[:n]) {
			log.Printf("got escape sequence: %x", buf)
			// We have downstream code that assumes full ANSI sequences, so we validate
			// here, I've never seen this fire
			panic("Got incomplete escape sequence")
		}

		c <- NewByteMsg(buf[:n])
	}

	// Close the channel
	close(c)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func prettyAnsiCsi(data []byte) (int, string) {
	// eat digits
	i := 2
	for ; i < len(data); i++ {
		if data[i] < '0' || data[i] > '9' {
			break
		}
	}

	if i == 2 {
		return 2, "CSI"
		//panic(fmt.Sprintf("Unknown CSI sequence, expected digits: %x", data))
	}
	if i == len(data) {
		return i, "CSI"
		//panic(fmt.Sprintf("Unknown CSI sequence, expected command: %x", data))
	}

	switch data[i] {
	case 'A':
		return i + 1, "CUP"
	case 'B':
		return i + 1, "CUD"
	case 'C':
		return i + 1, "CUF"
	case 'D':
		return i + 1, "CUB"
	case 'E':
		return i + 1, "CNL"
	case 'F':
		return i + 1, "CPL"
	case 'J':
		return i + 1, "ED"
	case 'K':
		return i + 1, "EL"
	case 'S':
		return i + 1, "SU"
	case 'm':
		return i + 1, "SGR"
	case 'n':
		if data[2] == '6' {
			return i + 2, "DSR"
		}
		panic("Unknown CSI sequence")
	}

	panic("Unknown CSI sequence")
}

func prettyAnsiC1(data []byte) (int, string) {
	// C1 codes
	switch data[1] {
	case '\x5b', '\x9b':
		if len(data) >= 3 {
			return prettyAnsiCsi(data)
		}
		return 2, "CSI"
	case '\x8e':
		return 2, "SSA"
	case '\x8f':
		return 2, "ESA"
	case '\x90':
		return 2, "DCS"
	case '\x9c':
		return 2, "ST"
	case '\x9d':
		return 2, "OSC"
	case '\x98':
		return 2, "SOS"
	case '\x9e':
		return 2, "PM"
	case '\x9f':
		return 2, "APC"
	case 'Q':
		return 2, "PU1"
	case 'R':
		return 2, "PU2"
	}

	return 2, "C1"
	//panic(fmt.Sprintf("Unknown C1 sequence: %x", data))
}

// Given a byte array, check if the beginning of the byte array is an ANSI
// escape sequence, if so, return the length of that sequence and the
// abbreviation.
func prettyAnsi(data []byte) (int, string) {
	if data == nil || len(data) == 0 {
		return 0, ""
	}

	// C0 codes
	switch data[0] {
	case '\x00':
		return 1, "NUL"
	case '\x01':
		return 1, "SOH"
	case '\x02':
		return 1, "STX"
	case '\x03':
		return 1, "ETX"
	case '\x04':
		return 1, "EOT"
	case '\x05':
		return 1, "ENQ"
	case '\x06':
		return 1, "ACK"
	case '\x07':
		return 1, "BEL"
	case '\x08':
		return 1, "BS"
	case '\x09':
		return 1, "HT"
	case '\x0a':
		return 1, "LF"
	case '\x0b':
		return 1, "VT"
	case '\x0c':
		return 1, "FF"
	case '\x0d':
		return 1, "CR"
	case '\x0e':
		return 1, "SO"
	case '\x0f':
		return 1, "SI"
	case '\x10':
		return 1, "DLE"
	case '\x11':
		return 1, "DC1"
	case '\x12':
		return 1, "DC2"
	case '\x13':
		return 1, "DC3"
	case '\x14':
		return 1, "DC4"
	case '\x15':
		return 1, "NAK"
	case '\x16':
		return 1, "SYN"
	case '\x17':
		return 1, "ETB"
	case '\x18':
		return 1, "CAN"
	case '\x19':
		return 1, "EM"
	case '\x1a':
		return 1, "SUB"
	case '\x1b':
		if len(data) >= 2 {
			return prettyAnsiC1(data)
		}
		return 1, "ESC"
	case '\x1c':
		return 1, "FS"
	case '\x1d':
		return 1, "GS"
	case '\x1e':
		return 1, "RS"
	case '\x1f':
		return 1, "US"
	case '\x7f':
		return 1, "DEL"
	}

	return 0, ""
}

// Given a byte slice, return a string with 2 lines.
// First line: the bytes in hex
// Second line: the bytes in ascii, with ansi escape sequences described
// by their name/code
func prettyHex(data []byte) string {
	hexLine := ""
	asciiLine := ""

	i := 0
	for i < len(data) {
		n, name := prettyAnsi(data[i:])
		if n > 0 {
			// we have an ansi escape code
			hexLine += fmt.Sprintf("%x ", data[i:i+n])
			asciiLine += fmt.Sprintf("%s ", name)
			i += n
		} else {
			hexLine += fmt.Sprintf("%02x ", data[i])
			asciiLine += fmt.Sprintf("%c ", data[i])
			i++
		}

		for len(hexLine) < len(asciiLine) {
			hexLine += " "
		}
		for len(asciiLine) < len(hexLine) {
			asciiLine += " "
		}

	}

	return hexLine + "\n" + asciiLine
}
