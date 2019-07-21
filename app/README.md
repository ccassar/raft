## Test Application

This application is a simple skeleton showing basic features of the raft package in action.

Applications running in a cluster will all see a single version of a distributed log dumped to stdout and can each
contribute to that log. The dummy messages include the originating node and a UUID.

In order to run one instance in the application cluster, with debug/logging written to file, you might invoke the
application as follows:

```
./app -config=app.json  -localNode=2 -zapFile=/tmp/log -debug
```

The output generated to stdout looks as follows:

```

./app -config=test/app.json -localNode=0 -debug 2>/tmp/log0.redir
Node0:29463c76-159a-43bd-8aa0-c0c654e67f69
Node0:832a806b-2cab-47d5-9a79-2a075f56324e
Node1:54caaacb-b585-4b01-8db4-9c3739d1c4ba
Node2:2740efcb-df3f-4c04-b3d0-b1c7ed163bc3
Node0:d6adc157-27c3-45de-9c91-f4d83ea2d19f
Node1:00bdafa3-30d4-4be8-bb06-3472927ad00a
Node0:3c732fc7-a31e-4a8d-8a01-c8199df058fd
...
```

By default, application logs to stderr, so redirecting stderr to file achieves the same end as `-zapFile` command line
option.

To build the docker image, from docker directory simply run the following:

```
# Replace tag with appropriate tag e.g. `git describe --always --dirty` or stable.
docker build -t raftapp:<tag> .
```

### Helm Deployment of Application Cluster to Kubernetes

If access to a kubernetes cluster is available, you can use the [helm chart provided](helm/raftapp) to deploy an application
cluster quickly and with little effort. If the prometheus operator is setup, you can monitor the test application
cluster using the dashboard provided. The simplest way of seeing the test application in action, is to run an application
cluster in the cloud; for example on Google Kubernetes Engine (GKE), especially if you already have access to a
kubernetes cluster with helm deployed.

Note:  The helm chart provided is simplistic and runs deployments. It is not a good example of how to deploy an application
(e.g a single helm chart install of a StatefulSet would probably be more appropriate than the rough and ready 3x single
replica Deployments with permanent volume claims), but it is sufficient to have the test application up and running with
dashboard installed for visibility.

##### GKE Cluster Prerequisites

You will need a Google Cloud account and to be running a cluster ([cheap, and possibly free, here](https://cloud.google.com/kubernetes-engine/docs/quickstart)).
At a minimum, in order to support simple deployment of the application with helm, helm and tiller must be setup on the
cluster. Again, thankfully, this is [easy and quick](https://docs.bitnami.com/kubernetes/get-started-gke/). Finally,
in order to be able to monitor metrics on test application as you would in production for a real application, you
may wish to deploy the (`prometheus-operator`)[https://github.com/helm/charts/tree/master/stable/prometheus-operator].
This will scrape the test application automatically and serve grafana dashboards for the application cluster (as well
as the underlying kubernetes cluster by default). The grafana admin account is set up by default with secret installed
here: `kubectl get secret --namespace default monitoring-grafana -o jsonpath="{.data.admin-password}" | base64 --decode `.
Needless to say, for a real deployment, you should change access account details.

Assuming the prometheus operator was deployed named `monitoring` in the `default` namespace as follows:
```
helm install --name monitoring stable/prometheus-operator
```

then port-forwarding to the local host can be setup on port 8090 for grafana, and 9090 for prometheus as follows if
ingress is not configured (and, by default, no ingress is set up for prometheus operator):

```
kubectl port-forward service/monitoring-grafana 8090:80
kubectl port-forward service/monitoring-prometheus-oper-prometheus 9090:9090
```


##### Deploying the Test Application on Google Kubernetes Engine

Once the prerequisite GKE cluster setup is in place and a project is available, build the test app container in
[docker](docker) directory locally as follows. PROJECT_ID is set to the gcloud project e.g. using
`gcloud config get-value project`.

Push the docker image to [Google Container Registry](https://cloud.google.com/container-registry/).

```
docker build -t gcr.io/${PROJECT_ID}/raftapp:stable .
docker push gcr.io/${PROJECT_ID}/raftapp:stable
```

Once the image is built and pushed to the registry, in order to run the three node applications cluster, simply run the
following deployments: 

```
for i in $(seq 0 2); do helm install raftapp --name ra$i --set nodeid=$i; done
```

To clean up the application cluster:

```
for i in $(seq 0 2); do helm delete ra$i --purge; done
```

Here are the resource you would see deployed in kubernetes for the running application cluster:

```
> kubectl get all,servicemonitor,pvc -l=app.kubernetes.io/name=raftapp
  NAME                               READY   STATUS    RESTARTS   AGE
  pod/ra0-raftapp-5584f55b79-qpl8z   1/1     Running   0          6m23s
  pod/ra1-raftapp-8f46578d8-frd65    1/1     Running   0          6m20s
  pod/ra2-raftapp-5c7bd956b9-kdpk4   1/1     Running   0          6m17s
  
  
  NAME                  TYPE        CLUSTER-IP     EXTERNAL-IP   PORT(S)               AGE
  service/ra0-raftapp   ClusterIP   10.117.7.153   <none>        10043/TCP,10042/TCP   6m24s
  service/ra1-raftapp   ClusterIP   10.117.12.92   <none>        10043/TCP,10042/TCP   6m21s
  service/ra2-raftapp   ClusterIP   10.117.14.27   <none>        10043/TCP,10042/TCP   6m18s
  
  
  NAME                          DESIRED   CURRENT   UP-TO-DATE   AVAILABLE   AGE
  deployment.apps/ra0-raftapp   1         1         1            1           6m24s
  deployment.apps/ra1-raftapp   1         1         1            1           6m21s
  deployment.apps/ra2-raftapp   1         1         1            1           6m18s
  
  NAME                                     DESIRED   CURRENT   READY   AGE
  replicaset.apps/ra0-raftapp-5584f55b79   1         1         1       6m25s
  replicaset.apps/ra1-raftapp-8f46578d8    1         1         1       6m22s
  replicaset.apps/ra2-raftapp-5c7bd956b9   1         1         1       6m19s
  
  NAME                                               AGE
  servicemonitor.monitoring.coreos.com/ra0-raftapp   6m
  servicemonitor.monitoring.coreos.com/ra1-raftapp   6m
  servicemonitor.monitoring.coreos.com/ra2-raftapp   6m
  
  NAME                                STATUS   VOLUME                                     CAPACITY   ACCESS MODES   STORAGECLASS   AGE
  persistentvolumeclaim/ra0-raftapp   Bound    pvc-c03f079f-abc3-11e9-a857-42010a80021c   2Gi        RWO            standard       6m26s
  persistentvolumeclaim/ra1-raftapp   Bound    pvc-c225cebc-abc3-11e9-a857-42010a80021c   2Gi        RWO            standard       6m23s
  persistentvolumeclaim/ra2-raftapp   Bound    pvc-c41cd913-abc3-11e9-a857-42010a80021c   2Gi        RWO            standard       6m20s

```


### Local Container Deployment

If access to GKE or equivalent is a problem, an application cluster can be run locally as containers quite easily and
quickly assuming docker is deployed:

```
docker run -dt --name ra0 --net=host -v ${PWD}/cfgdir:/root/cfg/ raftapp:stable /root/app --localNode=0 -debug -config=/root/cfg/app0.json
docker run -dt --name ra1 --net=host -v ${PWD}/cfgdir:/root/cfg/ raftapp:stable /root/app --localNode=1 -debug -config=/root/cfg/app1.json
docker run -dt --name ra2 --net=host -v ${PWD}/cfgdir:/root/cfg/ raftapp:stable /root/app --localNode=2 -debug -config=/root/cfg/app2.json
```

Note that in the example above, a file app<index>.json (with content similar to [app.json](test/app.json)) lives in the
mounted configuration directory providing the configuration files. The only change required across the different nodes'
configuration is the metrics endpoint port. Also note that in the example above we are sharing the host network stack.

If running daemonised (with `-d` instead of interactive `-i` option), attach to an instance as follows;

```
docker attach ra2 --no-stdin
```

Detaching from the daemonised application will leave the container running.