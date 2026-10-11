# Timothy Helm chart

Deploys the full Timothy stack on Kubernetes (EKS, AKS, GKE, kind, any
cluster with a NetworkPolicy-enforcing CNI and ReadWriteMany storage).

The deployment guide, including the EKS, AKS and GKE quick starts,
storage classes, workload identity and runtime class examples, lives on
the docs site: https://timothy-agent.github.io/docs/

```sh
helm install timothy deploy/helm/timothy --namespace timothy --create-namespace
```

`values.yaml` documents every knob inline.
