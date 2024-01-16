package main

import (
	"crypto/tls"
	"log"
	"os"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"rotation-demo/app"
)

func main() {
	// get env values; validation and error handling omitted for brevity
	hostPort := os.Getenv("TEMPORAL_ADDRESS")
	namespace := os.Getenv("TEMPORAL_NAMESPACE")
	clientCertPath := os.Getenv("TEMPORAL_TLS_CERT")
	clientKeyPath := os.Getenv("TEMPORAL_TLS_KEY")

	// Load the cert via the GetClientCertificate function in the ConnectionOptions of the Client
	temporalClient, err := client.Dial(client.Options{
		HostPort:  hostPort,
		Namespace: namespace,
		ConnectionOptions: client.ConnectionOptions{
			TLS: &tls.Config{
				GetClientCertificate: func(info *tls.CertificateRequestInfo) (*tls.Certificate, error) {
					log.Println("GetClientCertificate: loading X509 client cert and key")
					cert, err := tls.LoadX509KeyPair(clientCertPath, clientKeyPath)
					if err != nil {
						return nil, err
					}
					return &cert, nil
				},
			},
		},
	})
	if err != nil {
		log.Fatalln("Unable to connect to Temporal Cloud.", err)
	}
	defer temporalClient.Close()

	w := worker.New(temporalClient, "greeting-tasks", worker.Options{})

	w.RegisterWorkflow(app.GreetSomeone)

	err = w.Run(worker.InterruptCh())
	if err != nil {
		log.Fatalln("Unable to start worker", err)
	}
}
