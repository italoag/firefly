// Copyright © 2021 Kaleido, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package apiserver

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io/ioutil"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/kaleido-io/firefly/internal/apiroutes"
	"github.com/kaleido-io/firefly/internal/config"
	"github.com/kaleido-io/firefly/internal/engine"
	"github.com/stretchr/testify/assert"
)

func TestStartStopServer(t *testing.T) {
	config.Reset()
	config.Set(config.HttpPort, 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // server will immediately shut down
	err := Serve(ctx, false)
	assert.NoError(t, err)
}

func TestEnginInitFail(t *testing.T) {
	config.Reset()
	err := Serve(context.Background(), true)
	assert.Error(t, err)
}

func TestInvalidListener(t *testing.T) {
	config.Reset()
	config.Set(config.HttpAddress, "...")
	_, err := createListener(context.Background())
	assert.Error(t, err)
}

func TestServeFail(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	l.Close() // So server will fail
	s := &http.Server{}
	err := serveHTTP(context.Background(), l, s)
	assert.Error(t, err)
}

func TestMissingCAFile(t *testing.T) {
	config.Reset()
	config.Set(config.HttpTLSCAFile, "badness")
	r := mux.NewRouter()
	_, err := createServer(context.Background(), r)
	assert.Regexp(t, "FF10105", err.Error())
}

func TestBadCAFile(t *testing.T) {
	config.Reset()
	config.Set(config.HttpTLSCAFile, "../../test/config/firefly.core.yaml")
	r := mux.NewRouter()
	_, err := createServer(context.Background(), r)
	assert.Regexp(t, "FF10106", err.Error())
}

func TestTLSServerSelfSignedWithClientAuth(t *testing.T) {

	// Create an X509 certificate pair
	privatekey, _ := rsa.GenerateKey(rand.Reader, 2048)
	publickey := &privatekey.PublicKey
	var privateKeyBytes []byte = x509.MarshalPKCS1PrivateKey(privatekey)
	privateKeyFile, _ := ioutil.TempFile("", "key.pem")
	defer os.Remove(privateKeyFile.Name())
	privateKeyBlock := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: privateKeyBytes}
	pem.Encode(privateKeyFile, privateKeyBlock)
	serialNumber, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	x509Template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"Unit Tests"},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(100 * time.Second),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	derBytes, err := x509.CreateCertificate(rand.Reader, x509Template, x509Template, publickey, privatekey)
	assert.NoError(t, err)
	publicKeyFile, _ := ioutil.TempFile("", "cert.pem")
	defer os.Remove(publicKeyFile.Name())
	pem.Encode(publicKeyFile, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes})

	// Start up a listener configured for TLS Mutual auth
	config.Reset()
	config.Set(config.HttpTLSEnabled, true)
	config.Set(config.HttpTLSClientAuth, true)
	config.Set(config.HttpTLSKeyFile, privateKeyFile.Name())
	config.Set(config.HttpTLSCertFile, publicKeyFile.Name())
	config.Set(config.HttpTLSCAFile, publicKeyFile.Name())
	config.Set(config.HttpPort, 0)
	ctx, cancelCtx := context.WithCancel(context.Background())
	l, err := createListener(ctx)
	assert.NoError(t, err)
	r := mux.NewRouter()
	r.HandleFunc("/test", func(res http.ResponseWriter, req *http.Request) {
		res.WriteHeader(200)
		json.NewEncoder(res).Encode(map[string]interface{}{"hello": "world"})
	})
	s, err := createServer(ctx, r)
	assert.NoError(t, err)

	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		err := serveHTTP(ctx, l, s)
		assert.NoError(t, err)
		wg.Done()
	}()

	// Attempt a request, with a client certificate
	rootCAs := x509.NewCertPool()
	caPEM, _ := ioutil.ReadFile(publicKeyFile.Name())
	ok := rootCAs.AppendCertsFromPEM(caPEM)
	assert.True(t, ok)
	c := http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
					clientKeyPair, err := tls.LoadX509KeyPair(publicKeyFile.Name(), privateKeyFile.Name())
					return &clientKeyPair, err
				},
				RootCAs: rootCAs,
			},
		},
	}
	httpsAddr := fmt.Sprintf("https://%s/test", l.Addr().String())
	res, err := c.Get(httpsAddr)
	assert.NoError(t, err)
	if res != nil {
		assert.Equal(t, 200, res.StatusCode)
		var resBody map[string]interface{}
		json.NewDecoder(res.Body).Decode(&resBody)
		assert.Equal(t, "world", resBody["hello"])
	}

	// Close down the server and wait for it to complete
	cancelCtx()
	wg.Wait()
}

func TestJSONHTTPServePOST201(t *testing.T) {
	me := &engine.MockEngine{}
	handler := jsonHandler(me, &apiroutes.Route{
		Name:            "testRoute",
		Path:            "/test",
		Method:          "POST",
		JSONInputValue:  func() interface{} { return make(map[string]interface{}) },
		JSONOutputValue: func() interface{} { return make(map[string]interface{}) },
		JSONHandler: func(e engine.Engine, req *http.Request, input interface{}, output interface{}) (status int, err error) {
			assert.Equal(t, "value1", input.(map[string]interface{})["input1"])
			output.(map[string]interface{})["output1"] = "value2"
			return 201, nil
		},
	})
	s := httptest.NewServer(http.HandlerFunc(handler))
	defer s.Close()

	b, _ := json.Marshal(map[string]interface{}{"input1": "value1"})
	res, err := http.Post(fmt.Sprintf("http://%s/test", s.Listener.Addr()), "application/json", bytes.NewReader(b))
	assert.NoError(t, err)
	assert.Equal(t, 201, res.StatusCode)
	var resJSON map[string]interface{}
	json.NewDecoder(res.Body).Decode(&resJSON)
	assert.Equal(t, "value2", resJSON["output1"])
}

func TestJSONHTTPServeCustomGETError(t *testing.T) {
	me := &engine.MockEngine{}
	handler := jsonHandler(me, &apiroutes.Route{
		Name:            "testRoute",
		Path:            "/test",
		Method:          "GET",
		JSONInputValue:  func() interface{} { return nil },
		JSONOutputValue: func() interface{} { return make(map[string]interface{}) },
		JSONHandler: func(e engine.Engine, req *http.Request, input interface{}, output interface{}) (status int, err error) {
			assert.Equal(t, nil, input)
			return 503, fmt.Errorf("pop")
		},
	})
	s := httptest.NewServer(http.HandlerFunc(handler))
	defer s.Close()

	b, _ := json.Marshal(map[string]interface{}{"input1": "value1"})
	res, err := http.Post(fmt.Sprintf("http://%s/test", s.Listener.Addr()), "application/json", bytes.NewReader(b))
	assert.NoError(t, err)
	assert.Equal(t, 503, res.StatusCode)
	var resJSON map[string]interface{}
	json.NewDecoder(res.Body).Decode(&resJSON)
	assert.Equal(t, "pop", resJSON["error"])
}

func TestJSONHTTPResponseEncodeFail(t *testing.T) {
	me := &engine.MockEngine{}
	handler := jsonHandler(me, &apiroutes.Route{
		Name:            "testRoute",
		Path:            "/test",
		Method:          "GET",
		JSONInputValue:  func() interface{} { return nil },
		JSONOutputValue: func() interface{} { return make(map[string]interface{}) },
		JSONHandler: func(e engine.Engine, req *http.Request, input interface{}, output interface{}) (status int, err error) {
			output.(map[string]interface{})["unserializable"] = map[bool]interface{}{
				true: "not in JSON",
			}
			return 200, nil
		},
	})
	s := httptest.NewServer(http.HandlerFunc(handler))
	defer s.Close()

	b, _ := json.Marshal(map[string]interface{}{"input1": "value1"})
	res, err := http.Post(fmt.Sprintf("http://%s/test", s.Listener.Addr()), "application/json", bytes.NewReader(b))
	assert.NoError(t, err)
	var resJSON map[string]interface{}
	json.NewDecoder(res.Body).Decode(&resJSON)
	assert.Regexp(t, "FF10107", resJSON["error"])
}

func TestNotFound(t *testing.T) {
	handler := apiWrapper(notFoundHandler)
	s := httptest.NewServer(http.HandlerFunc(handler))
	defer s.Close()

	res, err := http.Get(fmt.Sprintf("http://%s/test", s.Listener.Addr()))
	assert.NoError(t, err)
	assert.Equal(t, 404, res.StatusCode)
	var resJSON map[string]interface{}
	json.NewDecoder(res.Body).Decode(&resJSON)
	assert.Regexp(t, "FF10109", resJSON["error"])
}
