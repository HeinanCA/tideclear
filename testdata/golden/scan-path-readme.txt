path: ./k8s
files scanned: 2
KIND        APIVERSION     NAMESPACE  NAME     REPLACEMENT  REMOVED IN  SEVERITY  ORIGIN
CronJob     batch/v1beta1  demo       nightly  batch/v1     1.25        BLOCKER   cron.yaml:0
Deployment  apps/v1beta1   demo       web      apps/v1      1.16        BLOCKER   deploy.yaml:0
