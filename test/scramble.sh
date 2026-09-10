#!/usr/bin/env bash

while true; do
    kubectl scale rs my-request-25-14-57-37 --replicas=$(( 1 + (RANDOM%5) ))
    sleep 50
    pods=($(kubectl get pods -l dual-pods.llm-d.ai/launcher-config-name -o jsonpath='{.items[*].metadata.name}'))
    npods=${#pods[*]}
    picked=${pods[$(( RANDOM % npods ))]}
    kubectl delete pod "$picked"
    sleep 50
done
