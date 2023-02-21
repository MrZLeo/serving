#! /usr/bin/bash

# Deploy cert-manager
kubectl apply -f ./third_party/cert-manager-latest/cert-manager.yaml
kubectl wait --for=condition=Established --all crd
kubectl wait --for=condition=Available -n cert-manager --all deployments

# Deploy serving
ko apply --selector knative.dev/crd-install=true -Rf config/core/
kubectl wait --for=condition=Established --all crd
ko apply -Rf config/core/

# Deploy Knative Ingress
kubectl apply -f ./third_party/kourier-latest/kourier.yaml

kubectl patch configmap/config-network \
  -n knative-serving \
  --type merge \
  -p '{"data":{"ingress.class":"kourier.ingress.networking.knative.dev"}}'

# Install and Configure Ingress Controller
kubectl apply \
  --filename https://projectcontour.io/quickstart/contour.yaml
kubectl rollout status ds envoy -n projectcontour
kubectl rollout status deploy contour -n projectcontour

# create an Ingress to Kourier Ingress Gateway
cat <<EOF | kubectl apply -n kourier-system -f -
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: kourier-ingress
  namespace: kourier-system
spec:
  rules:
  - http:
     paths:
       - path: /
         pathType: Prefix
         backend:
           service:
             name: kourier
             port:
               number: 80
EOF

# Configure Knative to use the kourier-ingress Gateway
ksvc_domain="\"data\":{\""$(hostname -I | awk '{ print$1 }')".nip.io\": \"\"}"
kubectl patch configmap/config-domain \
    -n knative-serving \
    --type merge \
    -p "{$ksvc_domain}"
