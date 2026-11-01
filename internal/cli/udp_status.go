package cli

import (
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"

	"github.com/ShaulLavo/brine/internal/apps"
)

func printUDPStatus(out io.Writer, item apps.Status) error {
	udp := item.UDP
	if udp == nil {
		return nil
	}
	endpoint := "allocation not committed"
	if udp.HostPort != 0 {
		client := net.JoinHostPort(udp.ClientAddress, strconv.Itoa(int(udp.HostPort)))
		bind := net.JoinHostPort(udp.BindAddress, strconv.Itoa(int(udp.HostPort)))
		endpoint = fmt.Sprintf("%s %s, bind %s, container %d", udp.Exposure, client, bind, udp.ContainerPort)
	}
	reservations := make([]string, 0, len(udp.Reservations))
	for _, row := range udp.Reservations {
		text := fmt.Sprintf("%s %s at %s, incarnation %s", row.ID, row.State, net.JoinHostPort(row.Endpoint.BindAddress, strconv.Itoa(int(row.Endpoint.HostPort))), row.Incarnation)
		if row.Owner != "" {
			text += " owner " + row.Owner
		}
		reservations = append(reservations, text)
	}
	_, err := fmt.Fprintf(out, "  UDP: %s; reservations [%s]; local %s%s; remote %s; ready %t\n", endpoint, strings.Join(reservations, "; "), udp.Local, driftText(udp.LocalReasons), udp.Remote, item.Ready())
	return err
}
