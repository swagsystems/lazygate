package play

import (
	"net"
	"strings"

	"lazymc/proto"
)

// Display time for lobby titles: two keep-alive periods in ticks.
const displayTime = 10 * 20 * 2 // KEEP_ALIVE_INTERVAL * TICKS_PER_SECOND * 2

// SendTitle sends lobby title packets to the client.
//
// This shows the given text for two keep-alive periods. Use a newline for the
// subtitle. If an empty string is given, the title times are reset to
// default.
func SendTitle(client *proto.Client, clientInfo *proto.ClientInfo, conn net.Conn, text string) error {
	// Grab title and subtitle bits, matching Rust str::lines() semantics
	// (empty lines are dropped, trailing \r stripped).
	var lines []string
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSuffix(l, "\r")
		if l == "" {
			continue
		}
		lines = append(lines, l)
	}

	title := ""
	subtitle := ""
	if len(lines) > 0 {
		title = lines[0]
	}
	if len(lines) > 1 {
		subtitle = strings.Join(lines[1:], "\n")
	}

	protocol := clientInfo.GetProtocol()
	if isV1_20_1(protocol) {
		return sendTitleV1_20_1(client, conn, title, subtitle)
	}
	if protocol != nil && *protocol >= ProtocolV1_17 {
		return sendTitleV1_17(client, conn, title, subtitle)
	}
	return sendTitleV1_16_3(client, conn, title, subtitle)
}

func chatMessageBytes(text string) []byte {
	return proto.StringBytes(proto.ChatMessageJSON(text))
}

func titleTimes(fadeIn, stay, fadeOut int32) []byte {
	w := proto.NewPacketWriter()
	w.WriteInt32(fadeIn)
	w.WriteInt32(stay)
	w.WriteInt32(fadeOut)
	return w.Bytes()
}

func sendTitleV1_16_3(client *proto.Client, conn net.Conn, title, subtitle string) error {
	// Set title (action 0)
	w := proto.NewPacketWriter()
	w.WriteU8(0)
	w.Write(chatMessageBytes(title))
	if err := writePacket(client, conn, proto.NewRawPacket(V1_16_3Title, w.Bytes())); err != nil {
		return err
	}

	// Set subtitle (action 1)
	w = proto.NewPacketWriter()
	w.WriteU8(1)
	w.Write(chatMessageBytes(subtitle))
	if err := writePacket(client, conn, proto.NewRawPacket(V1_16_3Title, w.Bytes())); err != nil {
		return err
	}

	// Set title times (action 3)
	w = proto.NewPacketWriter()
	w.WriteU8(3)
	if title == "" && subtitle == "" {
		// Defaults: https://minecraft.wiki/w/Commands/title#Detail
		w.Write(titleTimes(10, 70, 20))
	} else {
		w.Write(titleTimes(0, displayTime, 0))
	}
	return writePacket(client, conn, proto.NewRawPacket(V1_16_3Title, w.Bytes()))
}

func sendTitleV1_17(client *proto.Client, conn net.Conn, title, subtitle string) error {
	// Set title text
	if err := writePacket(client, conn, proto.NewRawPacket(V1_17SetTitleText, chatMessageBytes(title))); err != nil {
		return err
	}

	// Set title subtitle
	if err := writePacket(client, conn, proto.NewRawPacket(V1_17SetTitleSubtitle, chatMessageBytes(subtitle))); err != nil {
		return err
	}

	// Set title times
	if title == "" && subtitle == "" {
		return writePacket(client, conn, proto.NewRawPacket(V1_17SetTitleTimes, titleTimes(10, 70, 20)))
	}
	return writePacket(client, conn, proto.NewRawPacket(V1_17SetTitleTimes, titleTimes(0, displayTime, 0)))
}

func sendTitleV1_20_1(client *proto.Client, conn net.Conn, title, subtitle string) error {
	if err := writePacket(client, conn, proto.NewRawPacket(V1_20_1SetTitleText, chatMessageBytes(title))); err != nil {
		return err
	}
	if err := writePacket(client, conn, proto.NewRawPacket(V1_20_1SetTitleSubtitle, chatMessageBytes(subtitle))); err != nil {
		return err
	}
	if title == "" && subtitle == "" {
		return writePacket(client, conn, proto.NewRawPacket(V1_20_1SetTitleTimes, titleTimes(10, 70, 20)))
	}
	return writePacket(client, conn, proto.NewRawPacket(V1_20_1SetTitleTimes, titleTimes(0, displayTime, 0)))
}
