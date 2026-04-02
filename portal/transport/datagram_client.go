package transport

import (
	"context"
	"net"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/rs/zerolog/log"

	"github.com/gosuda/portal/v2/types"
	"github.com/gosuda/portal/v2/utils"
)

type ClientDatagramState struct {
	Identity    types.Identity
	AccessToken string
}

type ClientDatagram struct {
	session *datagramSession
}

func NewClientDatagram(onReceiveError func(error)) *ClientDatagram {
	return &ClientDatagram{
		session: newDatagramSession(256, false, onReceiveError),
	}
}

func (d *ClientDatagram) RunLoop(
	ctx context.Context,
	currentState func() (ClientDatagramState, bool),
	open func(context.Context, ClientDatagramState) (*quic.Conn, error),
) {
	for {
		select {
		case <-ctx.Done():
			d.session.Stop("listener context closed")
			return
		default:
		}

		state, ok := currentState()
		if !ok {
			if !utils.SleepOrDone(ctx, time.Second) {
				d.session.Stop("listener context closed")
				return
			}
			continue
		}

		conn, err := open(ctx, state)
		if err != nil {
			log.Info().
				Err(err).
				Str("component", "sdk-datagram-plane").
				Str("address", state.Identity.Address).
				Msg("quic datagram plane unavailable; retrying")
			if !utils.SleepOrDone(ctx, 2*time.Second) {
				d.session.Stop("listener context closed")
				return
			}
			continue
		}

		log.Info().
			Str("component", "sdk-datagram-plane").
			Str("address", state.Identity.Address).
			Str("remote_addr", conn.RemoteAddr().String()).
			Msg("quic tunnel connected")

		recvDone, err := d.session.Bind(conn)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Info().
				Err(err).
				Str("component", "sdk-datagram-plane").
				Str("address", state.Identity.Address).
				Msg("quic datagram plane did not bind cleanly; retrying")
			if !utils.SleepOrDone(ctx, time.Second) {
				return
			}
			continue
		}

		select {
		case <-ctx.Done():
			d.session.Stop("listener context closed")
			return
		case <-recvDone:
		}

		if !utils.SleepOrDone(ctx, time.Second) {
			return
		}
	}
}

func (d *ClientDatagram) Accept(done <-chan struct{}) (types.DatagramFrame, error) {
	if d == nil || d.session == nil {
		return types.DatagramFrame{}, net.ErrClosed
	}

	select {
	case <-done:
		return types.DatagramFrame{}, net.ErrClosed
	case dg := <-d.session.incoming:
		return dg, nil
	}
}

func (d *ClientDatagram) Send(flowID uint32, payload []byte) error {
	if d == nil || d.session == nil {
		return net.ErrClosed
	}
	return d.session.Send(flowID, payload)
}

func (d *ClientDatagram) Connected() bool {
	return d != nil && d.session != nil && d.session.hasConnection()
}

func (d *ClientDatagram) Clear(reason string) {
	if d == nil || d.session == nil {
		return
	}
	d.session.Clear(reason)
}

func (d *ClientDatagram) Close() {
	if d == nil || d.session == nil {
		return
	}
	d.session.Stop("listener closed")
}
