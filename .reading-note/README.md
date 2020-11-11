## 思考
- 当前local集群要执行的内容，例如创建 cluster，user， 修改全局 settings 这种。
- 受管集群要做的操作例如下发 role 信息，添加节点，部署workload 这种。

wrangler context 是用于k8s api 扩展的内容。启动 rancher 时会启动大量资源的 informer。然后在 informer 里添加 handler。

## k8s 模板代码生成
https://github.com/kubernetes/code-generator
https://github.com/kubernetes/gengo

- client-gen
- conversion-gen
- deepcopy-gen
- defaulter-gen
- go-to-protobuf
- import-boss
- informer-gen
- lister-gen
- openapi-gen
- register-gen
- set-gen

使用code-generator生成crd controller - 杜天鹏的文章 - 知乎
https://zhuanlan.zhihu.com/p/99148110

## Rancher维护的核心库
wrangler-api
This repo holds generated wrangler controller for third party projects, namely core Kubernetes.

## 自定义资源
features 自定义资源连 schema 都没有。那定义它有什么用呢？
```yaml
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  creationTimestamp: "2020-11-06T08:35:41Z"
  generation: 1
  managedFields:
  - apiVersion: apiextensions.k8s.io/v1beta1
    fieldsType: FieldsV1
    fieldsV1:
      f:spec:
        f:conversion:
          .: {}
          f:strategy: {}
        f:group: {}
        f:names:
          f:kind: {}
          f:listKind: {}
          f:plural: {}
          f:singular: {}
        f:preserveUnknownFields: {}
        f:scope: {}
        f:version: {}
        f:versions: {}
      f:status:
        f:storedVersions: {}
    manager: ___buildAndRun
    operation: Update
    time: "2020-11-06T08:35:41Z"
  - apiVersion: apiextensions.k8s.io/v1
    fieldsType: FieldsV1
    fieldsV1:
      f:status:
        f:acceptedNames:
          f:kind: {}
          f:listKind: {}
          f:plural: {}
          f:singular: {}
        f:conditions: {}
    manager: kube-apiserver
    operation: Update
    time: "2020-11-06T08:35:42Z"
  name: features.management.cattle.io
  resourceVersion: "6866009"
  selfLink: /apis/apiextensions.k8s.io/v1/customresourcedefinitions/features.management.cattle.io
  uid: 070f9fa7-5522-439a-9df0-d75d814fac19
spec:
  conversion:
    strategy: None
  group: management.cattle.io
  names:
    kind: Feature
    listKind: FeatureList
    plural: features
    singular: feature
  preserveUnknownFields: true
  scope: Cluster
  versions:
  - name: v3
    served: true
    storage: true
status:
  acceptedNames:
    kind: Feature
    listKind: FeatureList
    plural: features
    singular: feature
  conditions:
  - lastTransitionTime: "2020-11-06T08:35:42Z"
    message: no conflicts found
    reason: NoConflicts
    status: "True"
    type: NamesAccepted
  - lastTransitionTime: "2020-11-06T08:35:42Z"
    message: the initial names have been accepted
    reason: InitialNamesAccepted
    status: "True"
    type: Established
  storedVersions:
  - v3
```

## norman
一级 controller， 只有 controller 的外皮有 queue， 但是没有 handler 定义。types 库里面同时有 k8s 的 api controller 和 cattle 的 api rest client。还有个 compose 里面似乎是一个获取所有的 client 的索引。这些定义在 norman 包
```go
baseCattle  = "client"
baseK8s     = "apis"
baseCompose = "compose"
```
例如 settings controller, 还有 project node 等等很多。这些controller 是可以被启动的
```go
type SettingController interface {
	Generic() controller.GenericController
	Informer() cache.SharedIndexInformer
	Lister() SettingLister
	AddHandler(ctx context.Context, name string, handler SettingHandlerFunc)
	AddFeatureHandler(ctx context.Context, enabled func() bool, name string, sync SettingHandlerFunc)
	AddClusterScopedHandler(ctx context.Context, name, clusterName string, handler SettingHandlerFunc)
	AddClusterScopedFeatureHandler(ctx context.Context, enabled func() bool, name, clusterName string, handler SettingHandlerFunc)
	Enqueue(namespace, name string)
	EnqueueAfter(namespace, name string, after time.Duration)
	Sync(ctx context.Context) error
	Start(ctx context.Context, threadiness int) error
}
```
此外，所有的 rancher 自定义对象定义在 type/api 中，例如：apis/management.cattle.io/v3/zz_generated_setting_controller.go:66

## wrangler
- 二级 controller，也就是 handler。
- 生成 crd yaml 文件。
- schemas 结构定义， scheme 注册到一次。

代码中提到`Handler is the controller implementation for Foo resources`. 一个 handler 里包含多个（controller）的实现代码。

## types
rancher 中所有自定义资源的定义。依赖 wrangler， norman。

## steve
mux 下对应的 handler。
