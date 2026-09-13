package model

import "time"

// RandomBeautyPool 随机美图预审核图池表（random_beauty_pool）。
//
// 存储「候选 → 下载 → vision 审核」全流程通过后的成品图：
//   - 图片字节存 RustFS（对象存储），本表只存对象键与元数据；
//   - 用户取图毫秒级从本表随机命中，安全审核成本全部前置到补图后台；
//   - ImageKey 即上游候选 LocalPath，唯一索引用于查重，防止同一作品重复入库。
type RandomBeautyPool struct {
	ID          uint      `json:"id"           gorm:"primaryKey;autoIncrement;comment:图片ID"`
	ImageKey    string    `json:"image_key"    gorm:"not null;size:512;uniqueIndex;comment:上游候选 LocalPath（唯一，查重键）"`
	IllustID    int64     `json:"illust_id"    gorm:"not null;comment:Pixiv 作品ID"`
	Title       string    `json:"title"        gorm:"size:256;comment:作品标题"`
	Author      string    `json:"author"       gorm:"size:256;comment:画师名"`
	Tags        string    `json:"tags"         gorm:"type:text;not null;default:'[]';comment:作品标签（JSON数组字符串，审计用）"`
	ObjectKey   string    `json:"object_key"   gorm:"not null;size:128;comment:RustFS对象键（内容寻址）"`
	Mime        string    `json:"mime"         gorm:"size:64;comment:图片MIME类型"`
	Width       int       `json:"width"        gorm:"comment:图片宽度"`
	Height      int       `json:"height"       gorm:"comment:图片高度"`
	SizeBytes   int64     `json:"size_bytes"   gorm:"comment:图片字节数"`
	ModeratedAt time.Time `json:"moderated_at" gorm:"comment:审核通过时间"`
	CreatedAt   time.Time `json:"created_at"   gorm:"autoCreateTime;comment:创建时间"`
}

// TableName 指定表名。
func (RandomBeautyPool) TableName() string { return "random_beauty_pool" }
