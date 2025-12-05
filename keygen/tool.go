package main

import "fmt"

func main() {
	// ⚠️⚠️⚠️ 请在这里填入你【真实】的数据库连接字符串
	// 建议使用之前创建的【只读账号】 client_reader
	realDSN := "app_licenses:RcEs8Rx4fPXmcjzP@tcp(189.1.224.239:23306)/yjzs?charset=utf8mb4&parseTime=True&loc=Local"

	// 混淆密钥 (这个Key必须和下面主程序里的保持一致)
	key := []byte("MyObfuscationKey2025")

	fmt.Println("// --- 请将下面这段代码复制到 main.go 的配置区域 ---")
	fmt.Print("var dbDsnSecret = []byte{")
	for i := 0; i < len(realDSN); i++ {
		if i%10 == 0 {
			fmt.Print("\n\t")
		}
		encrypted := realDSN[i] ^ key[i%len(key)]
		fmt.Printf("0x%02x, ", encrypted)
	}
	fmt.Println("\n}")
}
