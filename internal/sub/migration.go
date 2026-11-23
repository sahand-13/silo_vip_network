package sub

import (
	"crypto/tls"
	_ "embed"
	"errors"
	"html/template"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/web/network"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

//go:embed migration.html
var migrationHTML string

var migrationPage = template.Must(template.New("subscription-migration").Parse(migrationHTML))

func StartMigrationServer() (*http.Server, error) {
	settings := &service.SettingService{}
	oldURL, newURL, err := settings.GetSubMigrationURLs()
	if err != nil || oldURL == "" || newURL == "" {
		return nil, err
	}
	port, path, err := settings.GetSubMigrationEndpoint()
	if err != nil || port == 0 {
		return nil, err
	}
	certFile, keyFile, err := settings.GetSubMigrationTLS()
	if err != nil {
		return nil, err
	}
	certificates := []tls.Certificate(nil)
	if certFile != "" || keyFile != "" {
		certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, err
		}
		certificates = append(certificates, certificate)
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("", strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	server := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if strings.TrimSuffix(request.URL.Path, "/") != strings.TrimSuffix(path, "/") {
				http.NotFound(writer, request)
				return
			}
			if request.Method != http.MethodGet && request.Method != http.MethodHead {
				writer.Header().Set("Allow", "GET, HEAD")
				writer.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			writer.Header().Set("Cache-Control", "no-store")
			writer.Header().Set("Referrer-Policy", "no-referrer")
			writer.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; form-action 'none'; base-uri 'none'; frame-ancestors 'none'")
			writer.Header().Set("Content-Type", "text/html; charset=utf-8")
			_ = migrationPage.Execute(writer, struct{ OldURL, NewURL string }{oldURL, newURL})
		}),
	}
	if len(certificates) != 0 {
		server.TLSConfig = &tls.Config{Certificates: certificates, MinVersion: tls.VersionTLS12}
		go func() {
			if err := server.ServeTLS(listener, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error("Subscription migration server stopped unexpectedly:", err)
			}
		}()
	} else {
		go network.ServeHTTP(server, listener, "Subscription migration server")
	}
	return server, nil
}
