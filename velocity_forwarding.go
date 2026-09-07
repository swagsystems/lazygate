package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"

	"lazymc/proto"
)

const (
	velocityPlayerInfoChannel = "velocity:player_info"
	velocityModernDefault     = int32(1)
	velocityModernLazySession = int32(4)
	minimumForwardingSecret   = 16
	maximumForwardingSecret   = 4096
)

func forwardingSecret(config *Config) ([]byte, error) {
	path := config.Auth.ForwardingSecretFile
	if path == "" {
		credentialsDir := os.Getenv("CREDENTIALS_DIRECTORY")
		if credentialsDir == "" {
			return nil, errors.New("forwarding secret is not configured")
		}
		path = filepath.Join(credentialsDir, "forwarding-secret")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat forwarding secret: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("forwarding secret must be a private regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read forwarding secret: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maximumForwardingSecret+1))
	if err != nil {
		return nil, fmt.Errorf("read forwarding secret: %w", err)
	}
	if len(data) > maximumForwardingSecret {
		return nil, errors.New("forwarding secret is too large")
	}
	data = bytes.TrimRight(data, "\r\n")
	if len(data) < minimumForwardingSecret {
		return nil, errors.New("forwarding secret is too short")
	}
	return data, nil
}

func respondVelocityForwarding(client *proto.Client, conn net.Conn, request proto.LoginPluginRequest, profile *proto.GameProfile, peer net.Addr, config *Config) error {
	if request.Channel != velocityPlayerInfoChannel {
		return errors.New("not a Velocity forwarding request")
	}
	if profile == nil {
		return errors.New("cannot forward an unauthenticated profile")
	}
	secret, err := forwardingSecret(config)
	if err != nil {
		return err
	}

	address := peer.String()
	if host, _, err := net.SplitHostPort(address); err == nil {
		address = host
	}
	if strings.TrimSpace(address) == "" {
		return errors.New("client address is unavailable")
	}

	forwardingVersion := velocityModernDefault
	if len(request.Data) == 1 && request.Data[0] >= byte(velocityModernDefault) {
		forwardingVersion = int32(request.Data[0])
		if forwardingVersion > velocityModernLazySession {
			forwardingVersion = velocityModernLazySession
		}
	}
	payload := proto.NewPacketWriter()
	payload.WriteVarInt(forwardingVersion)
	payload.WriteString(address)
	payload.Write(profile.UUID[:])
	payload.WriteString(profile.Name)
	payload.WriteVarInt(int32(len(profile.Properties)))
	for _, property := range profile.Properties {
		payload.WriteString(property.Name)
		payload.WriteString(property.Value)
		payload.WriteBool(property.Signature != nil)
		if property.Signature != nil {
			payload.WriteString(*property.Signature)
		}
	}

	mac := hmac.New(sha256.New, secret)
	mac.Write(payload.Bytes())
	responseData := append(mac.Sum(nil), payload.Bytes()...)

	w := proto.NewPacketWriter()
	w.WriteVarInt(request.MessageID)
	w.WriteBool(true)
	w.Write(responseData)
	return writePacketTo(client, conn, proto.NewRawPacket(proto.PacketServerLoginPluginResponse, w.Bytes()))
}
