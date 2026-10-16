module github.com/qiyuan2403/Q_RP_dsh/android/jni

go 1.27.1

require (
	github.com/qiyuan2403/Q_RP_dsh v0.0.0
	github.com/qiyuan2403/Y_Cordis v0.0.2
)

require gopkg.in/yaml.v3 v3.0.1 // indirect

replace github.com/qiyuan2403/Q_RP_dsh => ../..

replace github.com/qiyuan2403/Y_Cordis => /root/repos/Y_Cordis
