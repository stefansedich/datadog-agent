// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.36.3
//  protoc
// source: pkg/eventmonitor/proto/api/api.proto

package api

import (
	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
	reflect "reflect"
	sync "sync"
)

const (
	// Verify that this generated code is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(20 - protoimpl.MinVersion)
	// Verify that runtime/protoimpl is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(protoimpl.MaxVersion - 20)
)

type GetProcessEventParams struct {
	state          protoimpl.MessageState `protogen:"open.v1"`
	TimeoutSeconds int32                  `protobuf:"varint,1,opt,name=TimeoutSeconds,proto3" json:"TimeoutSeconds,omitempty"`
	unknownFields  protoimpl.UnknownFields
	sizeCache      protoimpl.SizeCache
}

func (x *GetProcessEventParams) Reset() {
	*x = GetProcessEventParams{}
	mi := &file_pkg_eventmonitor_proto_api_api_proto_msgTypes[0]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *GetProcessEventParams) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GetProcessEventParams) ProtoMessage() {}

func (x *GetProcessEventParams) ProtoReflect() protoreflect.Message {
	mi := &file_pkg_eventmonitor_proto_api_api_proto_msgTypes[0]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GetProcessEventParams.ProtoReflect.Descriptor instead.
func (*GetProcessEventParams) Descriptor() ([]byte, []int) {
	return file_pkg_eventmonitor_proto_api_api_proto_rawDescGZIP(), []int{0}
}

func (x *GetProcessEventParams) GetTimeoutSeconds() int32 {
	if x != nil {
		return x.TimeoutSeconds
	}
	return 0
}

type ProcessEventMessage struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Data          []byte                 `protobuf:"bytes,1,opt,name=Data,proto3" json:"Data,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ProcessEventMessage) Reset() {
	*x = ProcessEventMessage{}
	mi := &file_pkg_eventmonitor_proto_api_api_proto_msgTypes[1]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ProcessEventMessage) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ProcessEventMessage) ProtoMessage() {}

func (x *ProcessEventMessage) ProtoReflect() protoreflect.Message {
	mi := &file_pkg_eventmonitor_proto_api_api_proto_msgTypes[1]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ProcessEventMessage.ProtoReflect.Descriptor instead.
func (*ProcessEventMessage) Descriptor() ([]byte, []int) {
	return file_pkg_eventmonitor_proto_api_api_proto_rawDescGZIP(), []int{1}
}

func (x *ProcessEventMessage) GetData() []byte {
	if x != nil {
		return x.Data
	}
	return nil
}

var File_pkg_eventmonitor_proto_api_api_proto protoreflect.FileDescriptor

var file_pkg_eventmonitor_proto_api_api_proto_rawDesc = []byte{
	0x0a, 0x24, 0x70, 0x6b, 0x67, 0x2f, 0x65, 0x76, 0x65, 0x6e, 0x74, 0x6d, 0x6f, 0x6e, 0x69, 0x74,
	0x6f, 0x72, 0x2f, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x2f, 0x61, 0x70, 0x69, 0x2f, 0x61, 0x70, 0x69,
	0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x12, 0x03, 0x61, 0x70, 0x69, 0x22, 0x3f, 0x0a, 0x15, 0x47,
	0x65, 0x74, 0x50, 0x72, 0x6f, 0x63, 0x65, 0x73, 0x73, 0x45, 0x76, 0x65, 0x6e, 0x74, 0x50, 0x61,
	0x72, 0x61, 0x6d, 0x73, 0x12, 0x26, 0x0a, 0x0e, 0x54, 0x69, 0x6d, 0x65, 0x6f, 0x75, 0x74, 0x53,
	0x65, 0x63, 0x6f, 0x6e, 0x64, 0x73, 0x18, 0x01, 0x20, 0x01, 0x28, 0x05, 0x52, 0x0e, 0x54, 0x69,
	0x6d, 0x65, 0x6f, 0x75, 0x74, 0x53, 0x65, 0x63, 0x6f, 0x6e, 0x64, 0x73, 0x22, 0x29, 0x0a, 0x13,
	0x50, 0x72, 0x6f, 0x63, 0x65, 0x73, 0x73, 0x45, 0x76, 0x65, 0x6e, 0x74, 0x4d, 0x65, 0x73, 0x73,
	0x61, 0x67, 0x65, 0x12, 0x12, 0x0a, 0x04, 0x44, 0x61, 0x74, 0x61, 0x18, 0x01, 0x20, 0x01, 0x28,
	0x0c, 0x52, 0x04, 0x44, 0x61, 0x74, 0x61, 0x32, 0x65, 0x0a, 0x15, 0x45, 0x76, 0x65, 0x6e, 0x74,
	0x4d, 0x6f, 0x6e, 0x69, 0x74, 0x6f, 0x72, 0x69, 0x6e, 0x67, 0x4d, 0x6f, 0x64, 0x75, 0x6c, 0x65,
	0x12, 0x4c, 0x0a, 0x10, 0x47, 0x65, 0x74, 0x50, 0x72, 0x6f, 0x63, 0x65, 0x73, 0x73, 0x45, 0x76,
	0x65, 0x6e, 0x74, 0x73, 0x12, 0x1a, 0x2e, 0x61, 0x70, 0x69, 0x2e, 0x47, 0x65, 0x74, 0x50, 0x72,
	0x6f, 0x63, 0x65, 0x73, 0x73, 0x45, 0x76, 0x65, 0x6e, 0x74, 0x50, 0x61, 0x72, 0x61, 0x6d, 0x73,
	0x1a, 0x18, 0x2e, 0x61, 0x70, 0x69, 0x2e, 0x50, 0x72, 0x6f, 0x63, 0x65, 0x73, 0x73, 0x45, 0x76,
	0x65, 0x6e, 0x74, 0x4d, 0x65, 0x73, 0x73, 0x61, 0x67, 0x65, 0x22, 0x00, 0x30, 0x01, 0x42, 0x1c,
	0x5a, 0x1a, 0x70, 0x6b, 0x67, 0x2f, 0x65, 0x76, 0x65, 0x6e, 0x74, 0x6d, 0x6f, 0x6e, 0x69, 0x74,
	0x6f, 0x72, 0x2f, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x2f, 0x61, 0x70, 0x69, 0x62, 0x06, 0x70, 0x72,
	0x6f, 0x74, 0x6f, 0x33,
}

var (
	file_pkg_eventmonitor_proto_api_api_proto_rawDescOnce sync.Once
	file_pkg_eventmonitor_proto_api_api_proto_rawDescData = file_pkg_eventmonitor_proto_api_api_proto_rawDesc
)

func file_pkg_eventmonitor_proto_api_api_proto_rawDescGZIP() []byte {
	file_pkg_eventmonitor_proto_api_api_proto_rawDescOnce.Do(func() {
		file_pkg_eventmonitor_proto_api_api_proto_rawDescData = protoimpl.X.CompressGZIP(file_pkg_eventmonitor_proto_api_api_proto_rawDescData)
	})
	return file_pkg_eventmonitor_proto_api_api_proto_rawDescData
}

var file_pkg_eventmonitor_proto_api_api_proto_msgTypes = make([]protoimpl.MessageInfo, 2)
var file_pkg_eventmonitor_proto_api_api_proto_goTypes = []any{
	(*GetProcessEventParams)(nil), // 0: api.GetProcessEventParams
	(*ProcessEventMessage)(nil),   // 1: api.ProcessEventMessage
}
var file_pkg_eventmonitor_proto_api_api_proto_depIdxs = []int32{
	0, // 0: api.EventMonitoringModule.GetProcessEvents:input_type -> api.GetProcessEventParams
	1, // 1: api.EventMonitoringModule.GetProcessEvents:output_type -> api.ProcessEventMessage
	1, // [1:2] is the sub-list for method output_type
	0, // [0:1] is the sub-list for method input_type
	0, // [0:0] is the sub-list for extension type_name
	0, // [0:0] is the sub-list for extension extendee
	0, // [0:0] is the sub-list for field type_name
}

func init() { file_pkg_eventmonitor_proto_api_api_proto_init() }
func file_pkg_eventmonitor_proto_api_api_proto_init() {
	if File_pkg_eventmonitor_proto_api_api_proto != nil {
		return
	}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: file_pkg_eventmonitor_proto_api_api_proto_rawDesc,
			NumEnums:      0,
			NumMessages:   2,
			NumExtensions: 0,
			NumServices:   1,
		},
		GoTypes:           file_pkg_eventmonitor_proto_api_api_proto_goTypes,
		DependencyIndexes: file_pkg_eventmonitor_proto_api_api_proto_depIdxs,
		MessageInfos:      file_pkg_eventmonitor_proto_api_api_proto_msgTypes,
	}.Build()
	File_pkg_eventmonitor_proto_api_api_proto = out.File
	file_pkg_eventmonitor_proto_api_api_proto_rawDesc = nil
	file_pkg_eventmonitor_proto_api_api_proto_goTypes = nil
	file_pkg_eventmonitor_proto_api_api_proto_depIdxs = nil
}
