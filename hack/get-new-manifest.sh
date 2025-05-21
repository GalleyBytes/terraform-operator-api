#!/bin/bash

OUTPUT_FILE="$(mktemp)"
I3_BUNDLE="$(mktemp)"

printf 'apiVersion: v1
kind: List
metadata:
  resourceVersion: ""
  selfLink: ""

items:' >"$OUTPUT_FILE"

helm template infra3-virtual-cluster vcluster --repo https://charts.loft.sh -n namespace-placeholder |
    sed 's/^---//g' |
    sed 's/namespace-placeholder/\"{{ .namespace }}\"/g' |
    sed "s/^/  /g" |
    sed "s/^  #/- #/g" >>"$OUTPUT_FILE"

curl -s https://raw.githubusercontent.com/GalleyBytes/infra3/master/deploy/bundles/vTBD/vTBD.yaml |
    sed "s/^/      /g" >"$I3_BUNDLE"



printf "
📄\tTFO bundle saved at $I3_BUNDLE

📑\tVCluster manifest saved at $OUTPUT_FILE

Insert the TFO bundle under the VCluster's init--configmap 'data.manifests: |-'
"