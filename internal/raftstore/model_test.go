package raftstore

import "testing"

// TestPickQuant_Preferred 预算充足时保持首选量化。
func TestPickQuant_Preferred(t *testing.T) {
	q, err := PickQuant(7, 30<<30, "fp16") // 7B fp16 ≈ 18.2GB，30GB 预算充足
	if err != nil {
		t.Fatalf("PickQuant 失败: %v", err)
	}
	if q != "fp16" {
		t.Errorf("应保持 fp16，实际 %s", q)
	}
}

// TestPickQuant_AutoDowngrade 预算不足时自动降档。
func TestPickQuant_AutoDowngrade(t *testing.T) {
	// 7B 模型：fp16≈18.2GB、int8≈9.1GB、int4≈4.6GB
	// 预算 12GB：fp16 放不下 → 降 int8
	q, err := PickQuant(7, 12<<30, "fp16")
	if err != nil {
		t.Fatalf("PickQuant 失败: %v", err)
	}
	if q != "int8" {
		t.Errorf("预算 12GB 应自动降档 int8，实际 %s", q)
	}

	// 预算 6GB：fp16/int8 都放不下 → int4
	q, err = PickQuant(7, 6<<30, "fp16")
	if err != nil {
		t.Fatalf("PickQuant 失败: %v", err)
	}
	if q != "int4" {
		t.Errorf("预算 6GB 应自动降档 int4，实际 %s", q)
	}
}

// TestPickQuant_UnknownPreferred 未知档位回退 fp16 语义。
func TestPickQuant_UnknownPreferred(t *testing.T) {
	q, err := PickQuant(1, 8<<30, "int3") // 未知档位按 fp16 处理
	if err != nil {
		t.Fatalf("PickQuant 失败: %v", err)
	}
	if q != "fp16" {
		t.Errorf("未知首选档位应按 fp16 处理，实际 %s", q)
	}
}

// TestPickQuant_NoFit 所有档位都不满足时返回错误。
func TestPickQuant_NoFit(t *testing.T) {
	if _, err := PickQuant(70, 8<<30, "fp16"); err == nil {
		t.Fatal("70B 模型在 8GB 预算下应报错（最低 int4 ≈ 45GB）")
	}
}

// TestEstimateVRAM_Tiers 显存估算随量化档位单调递减（见 store_test.go 基础用例）。
func TestEstimateVRAM_Tiers(t *testing.T) {
	fp16 := EstimateVRAM(7, "fp16")
	int8 := EstimateVRAM(7, "int8")
	int4 := EstimateVRAM(7, "int4")
	if !(fp16 > int8 && int8 > int4) {
		t.Errorf("显存估算应随量化递减: fp16=%d int8=%d int4=%d", fp16, int8, int4)
	}
}

// TestModel_ValidateShard 分片参数校验。
func TestModel_ValidateShard(t *testing.T) {
	m := &Model{Name: "m", Path: "/p", VRAMBytes: 1024, TensorParallel: -1}
	if err := m.Validate(); err == nil {
		t.Error("负分片参数应校验失败")
	}
	m = &Model{Name: "m", Path: "/p", VRAMBytes: 1024, TensorParallel: 4}
	if err := m.Validate(); err != nil {
		t.Errorf("合法分片参数不应报错: %v", err)
	}
}
