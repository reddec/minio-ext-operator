# minio-ext-operator

Creates bucket, user, and policy in Minio.

## Description

Reflects CRD as Minio objects.

**Create user**

```yaml
apiVersion: minio.k8s.reddec.net/v1alpha1
kind: User
metadata:
  name: user-sample # user name in minio
spec:
  secretName: my-user # optional, default to <CRD-name>-minio
```

Secret contains:

- `AWS_ACCESS_KEY_ID`
- `AWS_SECRET_ACCESS_KEY`


**Create bucket**

```yaml
apiVersion: minio.k8s.reddec.net/v1alpha1
kind: Bucket
metadata:
  name: bucket-sample # bucket name in minio
spec:
  retain: false # optional (default: false) - do not remove bucket after CRD removal
  public: false # optional (default: false) - allow anonymous GetObject (download only)
```

- even if `public: true` directory listing is not allowed

**Create policy**

```yaml
apiVersion: minio.k8s.reddec.net/v1alpha1
kind: Policy
metadata:
  name: policy-sample
spec:
  bucket: public # bucket name
  user: my-user # username (key_id)
  read: false # read permissions
  write: true # write permissions
```

- `read: true` with `write: true` is special case and means all operations are allowed.

It is **namespaced** operator, which requires independent installation for each namespace. Check [example](example).

## Getting Started

### Install operator template

```bash
curl -L https://github.com/reddec/minio-ext-operator/releases/latest/download/minio-ext-operator.tar.gz | \
tar zxf -
```

### Setup environment and variables

- `kustomization.yaml` - namespace
- `patch_env.yaml` - secrets and endpoints
- `secrets.yaml` - minio admin user and password

Feel free to adjust kustomize as much as you wish

* Create manifests

## License

Copyright 2022 Aleksandr Baryshnikov.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

