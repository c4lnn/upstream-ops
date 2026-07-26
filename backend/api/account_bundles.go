package api

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bejix/upstream-ops/backend/accountbundle"
	"github.com/gin-gonic/gin"
)

const siteAccountBundleMultipartOverhead = 1 << 20

type siteAccountBundleExportInput struct {
	SiteIDs            []uint `json:"site_ids" binding:"required"`
	IncludeCredentials bool   `json:"include_credentials"`
	Password           string `json:"password"`
}

func registerSiteAccountBundles(g *gin.RouterGroup, d *Deps) {
	group := g.Group("/site-account-bundles")
	group.POST("/export", func(c *gin.Context) { exportSiteAccountBundle(c, d) })
	group.POST("/import/preview", func(c *gin.Context) { previewSiteAccountBundle(c, d) })
	group.POST("/import", func(c *gin.Context) { importSiteAccountBundle(c, d) })
}

func requireSiteAccountBundles(c *gin.Context, d *Deps) siteAccountBundleService {
	if d == nil || d.AccountBundles == nil {
		fail(c, http.StatusServiceUnavailable, errors.New("站点账号配置包服务未配置"))
		return nil
	}
	return d.AccountBundles
}

func exportSiteAccountBundle(c *gin.Context, d *Deps) {
	service := requireSiteAccountBundles(c, d)
	if service == nil {
		return
	}
	var input siteAccountBundleExportInput
	if err := c.ShouldBindJSON(&input); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	data, err := service.Export(c.Request.Context(), accountbundle.ExportOptions{
		SiteIDs: input.SiteIDs, IncludeCredentials: input.IncludeCredentials, Password: input.Password,
	})
	if err != nil {
		writeSiteAccountBundleError(c, err)
		return
	}
	filename := fmt.Sprintf("upstream-ops-site-accounts-%s.json", time.Now().UTC().Format("20060102-150405"))
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": filename})
	c.Header("Content-Disposition", disposition)
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "application/json; charset=utf-8", data)
}

func previewSiteAccountBundle(c *gin.Context, d *Deps) {
	service := requireSiteAccountBundles(c, d)
	if service == nil {
		return
	}
	data, options, _, err := readSiteAccountBundleImport(c, false)
	if err != nil {
		writeSiteAccountBundleError(c, err)
		return
	}
	plan, err := service.Preview(c.Request.Context(), data, options)
	if err != nil {
		writeSiteAccountBundleError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": plan})
}

func importSiteAccountBundle(c *gin.Context, d *Deps) {
	service := requireSiteAccountBundles(c, d)
	if service == nil {
		return
	}
	data, options, digest, err := readSiteAccountBundleImport(c, true)
	if err != nil {
		writeSiteAccountBundleError(c, err)
		return
	}
	result, err := service.Import(c.Request.Context(), data, options, digest)
	if err != nil {
		writeSiteAccountBundleError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": result})
}

func readSiteAccountBundleImport(c *gin.Context, requireDigest bool) ([]byte, accountbundle.ImportOptions, string, error) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, accountbundle.MaxBundleSize+siteAccountBundleMultipartOverhead)
	fileHeader, err := c.FormFile("file")
	if err != nil {
		if isRequestTooLarge(err) {
			return nil, accountbundle.ImportOptions{}, "", accountbundle.ErrBundleTooLarge
		}
		return nil, accountbundle.ImportOptions{}, "", errors.New("请选择站点账号配置包文件")
	}
	if c.Request.MultipartForm != nil {
		defer c.Request.MultipartForm.RemoveAll()
	}
	if fileHeader.Size > accountbundle.MaxBundleSize {
		return nil, accountbundle.ImportOptions{}, "", accountbundle.ErrBundleTooLarge
	}
	file, err := fileHeader.Open()
	if err != nil {
		return nil, accountbundle.ImportOptions{}, "", fmt.Errorf("打开站点账号配置包失败: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, accountbundle.MaxBundleSize+1))
	if err != nil {
		return nil, accountbundle.ImportOptions{}, "", fmt.Errorf("读取站点账号配置包失败: %w", err)
	}
	if len(data) > accountbundle.MaxBundleSize {
		return nil, accountbundle.ImportOptions{}, "", accountbundle.ErrBundleTooLarge
	}
	strategy := accountbundle.ImportStrategy(strings.TrimSpace(c.PostForm("strategy")))
	if strategy == "" {
		strategy = accountbundle.StrategyCreateOnly
	}
	digest := strings.TrimSpace(c.PostForm("digest"))
	if requireDigest && digest == "" {
		return nil, accountbundle.ImportOptions{}, "", errors.New("缺少预检 digest")
	}
	confirmBaseURLChanges := false
	if raw := strings.TrimSpace(c.PostForm("confirm_base_url_changes")); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, accountbundle.ImportOptions{}, "", errors.New("confirm_base_url_changes 必须是布尔值")
		}
		confirmBaseURLChanges = parsed
	}
	return data, accountbundle.ImportOptions{
		Strategy:              strategy,
		Password:              c.PostForm("password"),
		ConfirmBaseURLChanges: confirmBaseURLChanges,
	}, digest, nil
}

func isRequestTooLarge(err error) bool {
	return errors.Is(err, http.ErrBodyReadAfterClose) || strings.Contains(strings.ToLower(err.Error()), "request body too large")
}

func writeSiteAccountBundleError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, accountbundle.ErrBundleTooLarge):
		fail(c, http.StatusRequestEntityTooLarge, err)
	case errors.Is(err, accountbundle.ErrImportConflict), errors.Is(err, accountbundle.ErrPreviewStale):
		fail(c, http.StatusConflict, err)
	case errors.Is(err, accountbundle.ErrImportFailed):
		fail(c, http.StatusInternalServerError, err)
	default:
		fail(c, http.StatusBadRequest, err)
	}
}
