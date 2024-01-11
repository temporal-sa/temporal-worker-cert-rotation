# Temporal Worker mTLS Certificate Rotation

## Background
A Temporal cluster may use mTLS to authenticate Worker client connections.  This is the case with [Temporal Cloud](https://docs.temporal.io/cloud).  The Temporal Cloud docs provide instructions for generating client [certificates](https://docs.temporal.io/cloud/certificates) for mTLS authentication.  However, you will also need to plan for the rotation of your client certificates.

Certificate rotation can be done manually, although it is worth fully automating.  Also, it is desirable to rotate the certificates without restarting the Worker application.  A restart would result in clearing the Workflow [cache used in sticky execution](https://docs.temporal.io/workers#sticky-execution).  With an empty cache, a Workflow in progress would need to rebuild its state from scratch when the Workflow execution resumes.  The Worker would  retrieve the Workflow history from the Temporal server and replay the execution.  This is overhead that we would prefer to avoid. 

## Pre-requisites
This guide assumes that:
* You use Temporal Cloud (although this guide applies to self-hosted Temporal clusters too).
* You write Workers using the Temporal Go SDK (if not, see [What if I am not using the Go SDK?](#what-if-i-am-not-using-the-go-sdk) below).
* You deploy Worker applications to Kubernetes (if not, see [What if I am not using Kubernetes?](#what-if-i-am-not-using-kubernetes) below).

## Overview
The steps to setup certificate rotation for a Temporal Go Worker on Kubernetes are:
1. Create a CA certificate for your Temporal namespace
2. Install [cert-manager](https://cert-manager.io/) on your Kubernetes cluster
3. Configure a [CA Issuer](https://cert-manager.io/docs/configuration/ca/) to issue client certificates signed by the CA certificate
4. Install the cert-manager [csi-driver](https://cert-manager.io/docs/usage/csi/) to generate client certificates in Pod volumes
5. Configure your Temporal Worker to load the client certificate _**each time**_ the connection is established
6. Deploy your Temporal Worker Pod with the csi-driver volume

Let's go through each of these steps in detail.


### 1: Create a CA certificate
We will use [certstrap](https://github.com/square/certstrap) to bootstrap the CA certificate.  Certstrap is one of many tools that can create CAs.  It can also create and sign client certificates.  On our Kubernetes cluster, we will use cert-manager to create and sign the client certificates.  Off cluster we will create and sign client certs with certstrap (an optional step below).

Generate the CA Certificate:
```bash
certstrap init --common-name "Rotation Demo CA" --passphrase ""
```

If you have an existing Temporal namespace, [update the namespace with the new CA](https://docs.temporal.io/cloud/certificates#manage-certificates).

If you don't have an existing Temporal namespace, you can [create](https://docs.temporal.io/cloud/namespaces#create-a-namespace) one.  Supply the CA certficate when creating the namespace.  For example, using the `tcld namespace create` command:

```bash
# login to tcld cli
tcld login

# create the new namespace - this may take a few minutes
tcld namespace create --namespace rotation-demo \
    --region us-east-1 \
    --ca-certificate "$(cat ./out/Rotation_Demo_CA.crt | base64)"

# confirm the certificate for the namespace - my Temporal account id is 'sdvdw', yours will be different
tcld namespace ca list -n rotation-demo.sdvdw
[
    {
        "fingerprint": "c3020b30e7e016de334531788b54564b2125e975",
        "issuer": "CN=Rotation Demo CA",
        "subject": "CN=Rotation Demo CA",
        "notBefore": "2024-01-09T21:24:24Z",
        "notAfter": "2025-07-09T21:34:23Z",
        "base64EncodedData": "<omitted for brevity>"
    }
]
```

#### (Optional) Create a client certificate to use outside of Kubernetes, e.g. for the [temporal](https://docs.temporal.io/cli) CLI
```bash
# create a client certificate
certstrap request-cert --common-name rotation-demo-cli-client --passphrase ""

# sign the client certificate using the CA certificate
certstrap sign rotation-demo-cli-client --CA "Rotation Demo CA"

# test the setup using the `temporal` CLI
temporal operator namespace describe \
    --address rotation-demo.sdvdw.tmprl.cloud:7233 \
    --tls-cert-path ./out/rotation-demo-cli-client.crt \
    --tls-key-path ./out/rotation-demo-cli-client.key \
    rotation-demo.sdvdw

  # successful output will show the namespace info, e.g.
  NamespaceInfo.Name                    rotation-demo.sdvdw
  NamespaceInfo.Id                      4807be30-0b1d-47c2-ab3b-bb9c14660f0b  
  # additional output omitted for brevity
```


### 2. Install [cert-manager](https://cert-manager.io/)

Follow the instructions at https://cert-manager.io/docs/installation/ to install cert-manager on your Kubernetes cluster.

The [default static configuration](https://cert-manager.io/docs/installation/#default-static-install) worked for me.


### 3. Configure a [CA Issuer](https://cert-manager.io/docs/configuration/ca/)

First, create a Kubernetes Secret containing the CA certificate key pair:
```bash
kubectl create secret tls rotation-demo-ca-key-pair \
    --cert=out/Rotation_Demo_CA.crt \
    --key=out/Rotation_Demo_CA.key
```

Next, create a CA Issuer to issue client certificates signed by the CA certificate:
```bash
kubectl apply -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: rotation-demo-ca-issuer
spec:
  ca:
    secretName: rotation-demo-ca-key-pair
EOF
```


### 4. Install the cert-manager [csi-driver](https://cert-manager.io/docs/usage/csi/)
Follow the instructions at https://cert-manager.io/docs/usage/csi-driver/#installation to install the csi-driver on your Kubernetes cluster.

The `helm upgrade...` command worked for me. 


### 5. Configure your Temporal Worker

A Temporal Worker uses a Temporal Client to connect to the Temporal server.  And the Temporal Client requires a client certificate to authenticate to the Temporal server.  The connection is a long-lasting connection through which the Worker polls for tasks on a task queue.  

However, the Temporal Cloud frontend closes this connection every 5 minutes.  When the connection is closed, the Worker, using the Client, will establish a new connection.  (Note: in self-hosted clusters you change the 5m default using the `frontend.keepAliveMaxConnectionAge` parameter.)

In a typical Worker program, the certificate files are read once during the Worker initialization.  The certificate data is then supplied to the Client in the connection options.  When the Worker reconnects to Temporal Cloud (after 5 minutes), the same static certificate data will be used.  We see this pattern in the code snippet within the Go SDK developer's guide under [How to connect to Temporal Cloud](https://docs.temporal.io/dev-guide/go/foundations#connect-to-temporal-cloud):
```go
func main() {
    // Get the key and cert from your env or local machine
    clientKeyPath := "./secrets/yourkey.key"
    clientCertPath := "./secrets/yourcert.pem"
    ...

    // Use the crypto/tls package to create a cert object
    cert, err := tls.LoadX509KeyPair(clientCertPath, clientKeyPath)
    if err != nil {
        log.Fatalln("Unable to load cert and key pair.", err)
    }    

    // Add the cert to the tls certificates in the ConnectionOptions of the Client
    temporalClient, err := client.Dial(client.Options{
        HostPort:  hostPort,
        Namespace: namespace,
        ConnectionOptions: client.ConnectionOptions{
            TLS: &tls.Config{
                Certificates: []tls.Certificate{cert},
            },
        },
    })
    ...
}
```

In the above example, the `tls.Config` is constructed using 
the [Certificates](https://pkg.go.dev/crypto/tls#Config.Certificates) option.  The `cert`, created on line 7, will be used every time the connection is established.

However, the Go docs describe an alternative to `Certificates`:
> Clients doing client-authentication may set either Certificates or GetClientCertificate.

Using the [GetClientCertificate](https://pkg.go.dev/crypto/tls#Config.GetClientCertificate) option we can define a function that will be called each time the connection is created.  We can load the certificate data dynamically each time, rather than only once on initialization.  This allows us to achieve certificate rotation without requiring a restart of the Worker application.

Here is the code snippet above, updated to use `GetClientCertificate` instead of `Certificates`:
```go
func main() {
    // Get the key and cert from your env or local machine
    clientKeyPath := "./secrets/yourkey.key"
    clientCertPath := "./secrets/yourcert.pem"
    ...
    
    // Load the cert via the GetClientCertificate function in the ConnectionOptions of the Client
    temporalClient, err := client.Dial(client.Options{
        HostPort:  hostPort,
        Namespace: namespace,
        ConnectionOptions: client.ConnectionOptions{
            TLS: &tls.Config{
                GetClientCertificate: func(info *tls.CertificateRequestInfo) (*tls.Certificate, error) {
                    // Use the crypto/tls package to create a cert object
                    cert, err := tls.LoadX509KeyPair(clientCertPath, clientKeyPath)
                    if err != nil {
                        return nil, err
                    }
                    return &cert, nil
                },
            },
        },
    })
    ...
}
```

I have implemented the above approach in the [worker/main.go](./worker/main.go) file in this repo.


### 6. Deploy your Temporal Worker Pod with the csi-driver volume

The Go application in this repository is available as a Docker image at `pvsone/rotation-demo-worker-go:1.0.0`.

A sample Kubernetes Pod manifest is available at [manifests/pod.yaml](./manifests/pod.yaml).  The Pod uses the csi-driver to generate the client certificate files as a Pod volume.  The following [volume attributes](https://cert-manager.io/docs/usage/csi-driver/#supported-volume-attributes) are used to configure the csi-driver:
```yaml
        csi.cert-manager.io/issuer-name: rotation-demo-ca-issuer
        csi.cert-manager.io/common-name: rotation-demo-worker
        csi.cert-manager.io/duration: 5m
        csi.cert-manager.io/fs-group: "1000"
```

Based on the above settings, the cert-manager csi-driver will generate a client certificate with a common-name of `rotation-demo-worker` and a validity duration of 5 minutes.  The client certificate will be signed by the Issuer we created in step 3, `rotation-demo-ca-issuer`.  The FS group for the written files will be 1000.

Finally, the driver will keep track the certificate in order to monitor when it should be marked for renewal.  When this happens, the driver will request a new signed certificate, and overwrite the existing certificate in the path.  Magic!

Deploy the Pod:
```bash
kubectl apply -f manifests/pod.yaml
```

Once deployed, you can inspect the Pod filesystem to see that the certificate files in the `/certs` directory are overwritten before the 5 minute duration expires.

Additionally you can see from the Pod logs that the `GetClientCertificate` function is called every 5 minutes to load the updated certificate files:
```sh
...
2024/01/10 22:33:16 GetClientCertificate: loading X509 client cert and key
2024/01/10 22:38:17 GetClientCertificate: loading X509 client cert and key
2024/01/10 22:43:17 GetClientCertificate: loading X509 client cert and key
...
```

Feel free to run some tests before, during and after the 5 minute intervals.  You should not experience any connection failures due to certificate expiration or rotation. This repo does not include a starter program for the Workflow, but you can use the CLI as in:
```bash
temporal workflow execute --type GreetSomeone --task-queue greeting-tasks --input '"Rotey McRoteface"'
```

Success!  We have achieved certificate rotation without restarting the Worker application.


## What if I am not using the Go SDK?

1. Investigate if your language/SDK supports dynamic loading of the client certificate. I have not fully researched each language/SDK, it is on the TODO list :)

2. Consider if a rolling restart of your Worker Pods is acceptable.  For some use cases the overhead of terminating the Workers, and rebuilding the cache may not be a concern.

3. Run your non-Go Worker application along with a Go proxy sidecar.  The sidecar will handle the mTLS connection and the rotation of the client certificate.  I have implemented a [simple Temporal Go proxy](https://github.com/pvsone/temporal-grpc-proxy) that could easily be extended with `GetClientCertificate` approach in this guide.

4. Consider Istio, or another similar service mesh.  Istio can handle mTLS connections and certificate rotation, through their sidecar proxy.


## What if I am not using Kubernetes?

If your platform allows for application files to be overwritten while the application is running, then you can use the `GetClientCertificate` approach in this guide.  Otherwise, you may be forced to restart your Workers each time the certificate files are rotated.

