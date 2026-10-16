module github.com/qiyuan2403/Q_RP_dsh

go 1.27.1

require github.com/qiyuan2403/Y_Cordis v0.0.0

require gopkg.in/yaml.v3 v3.0.1 // indirect

// The kernel is a sibling repository during development. A release pins it to
// a real version instead; until then this keeps the two in lockstep.
replace github.com/qiyuan2403/Y_Cordis => ../Y_Cordis
