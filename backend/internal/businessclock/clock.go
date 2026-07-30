package businessclock

import (
	"time"
	_ "time/tzdata"
)

const Timezone = "Asia/Shanghai"

func Location() (*time.Location, error) {
	return time.LoadLocation(Timezone)
}
