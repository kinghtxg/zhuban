import { Button, Card, Collapse, Message, Space, Table, Tag, Typography } from '@arco-design/web-react';
import { IconPlayCircle, IconBook } from '@arco-design/web-react/icon';
import { tourControl } from '@/components/OnboardingTour';
import { downloadBlankTemplate } from '@/api/endpoints';
import { errorMessage } from '@/utils/format';

const { Title, Paragraph, Text } = Typography;

const CHEATSHEET = [
  {
    name: '文本变量',
    word: '{{ 项目名称 }}',
    json: '"项目名称": "某系统验收报告"',
    note: '键名允许中文；前后空格会被忽略',
  },
  {
    name: '默认值',
    word: '{{ 负责人 or "未指派" }}',
    json: '"负责人": "张三"',
    note: 'Jinja2 表达式，缺省时显示默认文案',
  },
  {
    name: '表格行循环',
    word: '{%tr for item in items %} … {{ item.name }} … {%tr endfor %}',
    json: '"items": [{"name": "第1条"}, {"name": "第2条"}]',
    note: '一行一行复制，最适合缺陷清单表',
  },
  {
    name: '整表/整段循环',
    word: '{% for item in items %} （段落+表格） {% endfor %}',
    json: '"items": [ {...}, {...} ]',
    note: '每个 item 复制一整段（含整张表）',
  },
  {
    name: '嵌套循环',
    word: '{% for g in 分组 %} … {%tr for it in g.条目 %} … {%tr endfor %} … {% endfor %}',
    json: '"分组": [{"分组": "账号与权限", "条目": [{"问题": "弱口令"}]}]',
    note: '组内数组挂在父项里，不是平级；支持两级',
  },
  {
    name: '条件块',
    word: '{% if level == "高" %} 高风险标记 {% endif %}',
    json: '"level": "高"',
    note: '比较值会被提取为枚举校验',
  },
  {
    name: '图片',
    word: '{{ image_logo }}',
    json: '"image_logo": {"type":"image","src":"data:image/png;base64,...","width":120,"height":80}',
    note: '宽高单位毫米；src 支持 http(s) 或 base64',
  },
  {
    name: '二维码',
    word: '{{ qr_报告编号 }}',
    json: '"qr_报告编号": {"type":"qrcode","text":"DEMO-2026-001","size":120}',
    note: 'size 为成品显示宽度（毫米）',
  },
  {
    name: '富文本',
    word: '{{ rich_修复建议 }}',
    json: '"rich_修复建议": {"type":"markdown","content":"**加粗** 或 - 列表"}',
    note: '支持 markdown / 受限 HTML（加粗、列表、颜色）',
  },
];

const ERROR_TABLE = [
  { code: 'template_disabled', meaning: '模板已停用', fix: '在模板库重新启用该模板，或换用其他模板编号' },
  { code: 'data_invalid', meaning: '数据校验失败（缺字段/类型不符）', fix: '按提示里的 path 检查 JSON；对照变量清单补齐必填项' },
  { code: 'template_invalid', meaning: 'Word 里的占位符被样式拆坏', fix: '在 Word 中删掉占位符，用纯文本重新输入一遍，不要刷样式' },
  { code: 'conversion_failed', meaning: 'LibreOffice 转 PDF 失败', fix: '重试一次；仍失败请联系管理员检查渲染服务' },
  { code: 'locked', meaning: '账号登录失败过多被临时锁定', fix: '等待几分钟后再试，或联系管理员' },
  { code: 'signature_unavailable', meaning: '引用的签名/印章未通过审核（V3）', fix: '等管理员审核通过，或换一枚已通过的素材' },
  { code: 'grant_denied', meaning: '签名/印章未授权给当前用户或 Key（V3）', fix: '联系 owner/admin 授予授权；reason 见 message（unauthorized/exhausted/expired/not_yet）' },
];

// 手册链接指向仓库内的 docs/manual。开源后默认走公开仓库，
// 不再写死某个人的内网 GitLab —— 那是部署环境的事，不该固化进产品代码。
const MANUAL_DOCS_URL =
  (import.meta.env.VITE_MANUAL_DOCS_URL as string | undefined) ||
  'https://github.com/kinghtxg/zhuban/tree/main/docs/manual';

// 内嵌手册章节数据：不引入 markdown 渲染器，用 TSX 组件手写。
function Code({ children }: { children: string }) {
  return (
    <pre className="df-codeblock" style={{ margin: '8px 0', padding: 12, whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>
      {children}
    </pre>
  );
}

export default function HelpPage() {
  const downloadBlank = async () => {
    try {
      const { blob, filename } = await downloadBlankTemplate();
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = filename || 'blank-template.docx';
      a.click();
      URL.revokeObjectURL(url);
    } catch (err) {
      Message.error(errorMessage(err).summary);
    }
  };

  return (
    <div className="df-page">
      <div className="df-page-header">
        <div>
          <h2 className="df-page-title">引导与帮助</h2>
          <div className="df-page-sub">用户手册、模板语法速查、空白模板下载与常见错误对照</div>
        </div>
        <Space>
          <Button icon={<IconPlayCircle />} onClick={() => tourControl.start()}>
            重新播放新手引导
          </Button>
          <Button type="primary" onClick={downloadBlank}>
            下载空白示例模板
          </Button>
        </Space>
      </div>

      {/* ============ 用户手册（内嵌完整章节） ============ */}
      <Card
        title={
          <Space>
            <IconBook />
            <span>用户手册</span>
            <Tag color="arcoblue">不离开本页即可看全</Tag>
          </Space>
        }
        extra={
          <Button type="text" href={MANUAL_DOCS_URL} target="_blank">
            完整手册（仓库 docs/manual/）→
          </Button>
        }
        style={{ marginBottom: 16 }}
      >
        <Paragraph type="secondary" style={{ marginBottom: 16 }}>
          铸版是「Word 模板 + JSON 数据 → docx / pdf」的渲染平台。下面是按主题整理的完整章节；
          要更深入（含完整数据流示例、curl/Python 示例）见仓库 docs/manual/。
        </Paragraph>

        <Collapse defaultActiveKey={['tpl']}>
          {/* ---- 模板制作规范 ---- */}
          <Collapse.Item header="一、如何制作一份版式规范的 Word 模板" name="tpl">
            <Title heading={6}>一条铁律</Title>
            <Paragraph>模板是什么格式，输出就是什么格式。页面纸张、页边距、页眉页脚、表格线、字体字号全部继承自你的 .docx，平台不会重排版。</Paragraph>

            <Title heading={6}>页面与表格</Title>
            <Table
              size="small"
              pagination={false}
              data={[
                { k: '纸张', v: 'A4；涉密报告按单位规范定页边距' },
                { k: '页眉页脚', v: '用 Word「插入→页眉页脚」，不要在正文手敲一行当页眉（跨页不重复）' },
                { k: '页码', v: '插入→页码→页面底端居中；不要手敲数字' },
                { k: '固定列宽', v: '表格属性→列→指定宽度；取消「自动重调尺寸以适应内容」' },
                { k: '跨页断行', v: '行属性→勾「允许跨页断行」；表头行勾「重复标题行」' },
              ]}
              columns={[
                { title: '项', dataIndex: 'k', width: 120 },
                { title: '怎么做', dataIndex: 'v' },
              ]}
            />

            <Title heading={6} style={{ marginTop: 16 }}>字体（渲染在 Linux 容器内 LibreOffice）</Title>
            <Paragraph>
              建议正文 <Text code>宋体/小四</Text>、标题 <Text code>黑体</Text>、表格内 <Text code>宋体/五号</Text>。
              容器预装思源宋/黑（Noto CJK），并兜底 simhei/msyh。若你用了容器没有的字体（方正小标宋、汉仪等），
              导出 PDF 会被静默替换到兜底字体——这不是 bug，改用「宋体/黑体/仿宋/楷体」四种通用名即可，或联系运维挂字体。
            </Paragraph>

            <Title heading={6}>占位符书写（最易踩坑）</Title>
            <Paragraph>
              在 Word 里切正文默认字体，<Text bold>一次性连续输入</Text> <Text code>{'{{ 项目编号 }}'}</Text>，
              中途别切字体、别退格重敲、别加批注/修订。否则 <Text>{'{{'}</Text> 与 <Text>{'}}'}</Text> 会被拆到不同 run，报
              <Tag color="red" size="small" style={{ marginLeft: 6 }}>template_invalid</Tag>。
              处理办法：删掉这几个字，纯文本重敲一遍。
            </Paragraph>
          </Collapse.Item>

          {/* ---- 占位符语法 ---- */}
          <Collapse.Item header="二、占位符语法全量（Word 写法 + JSON 对照）" name="syntax">
            <Table
              size="small"
              pagination={false}
              data={[
                { kind: '文本', word: '{{ 项目名称 }}', json: '"项目名称": "某系统验收报告"' },
                { kind: '默认值', word: '{{ 负责人 or "未指派" }}', json: '"负责人": "张三"' },
                { kind: '表格行循环', word: '{%tr for item in items %} … {{ item.name }} … {%tr endfor %}', json: '"items": [{"name":"第1条"},{"name":"第2条"}]' },
                { kind: '整段/整表循环', word: '{% for item in items %} … {% endfor %}', json: '"items": [ {...}, {...} ]' },
                { kind: '条件', word: '{% if 等级 == "高" %} 高风险 {% endif %}', json: '"等级": "高"' },
                { kind: '图片', word: '{{ image_logo }}', json: '"image_logo": {"type":"image","src":"...","width":40,"height":40}' },
                { kind: '二维码', word: '{{ qr_报告编号 }}', json: '"qr_报告编号": {"type":"qrcode","text":"DEMO-001","size":25}' },
                { kind: '富文本', word: '{{ rich_修复建议 }}', json: '"rich_修复建议": {"type":"markdown","content":"- 列表\\n**加粗**"}' },
              ]}
              columns={[
                { title: '能力', dataIndex: 'kind', width: 120, render: (v) => <Tag size="small">{v}</Tag> },
                { title: 'Word 写法', dataIndex: 'word', render: (v) => <Text code>{v}</Text> },
                { title: 'JSON 数据', dataIndex: 'json', render: (v) => <Text code style={{ fontSize: 12 }}>{v}</Text> },
              ]}
            />
            <Paragraph type="secondary" style={{ marginTop: 12 }}>
              命名前缀自动识别：<Text code>image_/img_</Text>=图片、<Text code>qr_</Text>=二维码、
              <Text code>rich_/html_/md_</Text>=富文本。图片/二维码宽高单位是毫米（mm），不是像素。
            </Paragraph>
          </Collapse.Item>

          {/* ---- 变量定义 ---- */}
          <Collapse.Item header="三、变量定义在哪里维护" name="vars">
            <Paragraph>
              模板上传后系统自动扫描出变量清单。可在两处维护（同一份数据）：
            </Paragraph>
            <Paragraph>
              ① <Text bold>模板详情 → 变量定义</Text>；② <Text bold>在线编辑器右侧「变量」面板</Text>（VariablesPanel）。
            </Paragraph>
            <Paragraph>可补：默认值、是否必填、字段说明。标记为必填的字段，业务方投递 JSON 缺失会被校验拦下并指出 path；
              条件块里的比较值会被收为枚举；系统据此自动生成示例 JSON，业务方照着填即可。</Paragraph>
          </Collapse.Item>

          {/* ---- 在线编辑器 ---- */}
          <Collapse.Item header="四、在线编辑器怎么用" name="editor">
            <Paragraph>模板中心 → 模板库 → 某模板 →「在线编辑」。可直接改段落/表格、插入占位符（文本/图片/二维码/富文本/签名/印章）、传模板资源、右侧实时预览。</Paragraph>
            <Paragraph>
              <Text bold>关键认知：实时预览（apply/preview）不扣签章次数</Text>——它真实渲染签章图给你看效果，但不消耗授权额度。
              真正扣次数发生在点「生成并下载」或业务方实际产出 docx/pdf 时。放心反复预览排版。
            </Paragraph>
            <Paragraph>保存即生成新版本（语义化递增），可在「版本」页签切换激活、一键回滚。「导出 pdf」是实际产出（无水印），预览页图带「铸版预览」水印仅供排版核对。</Paragraph>
          </Collapse.Item>

          {/* ---- 投递流程 ---- */}
          <Collapse.Item header="五、投递即生成（业务系统调用）" name="deliver">
            <Paragraph>主要入口 <Text code>POST /api/v1/generate</Text>，带 <Text code>X-API-Key</Text> 头。</Paragraph>
            <Code>{`curl -X POST '${window.location.origin}/api/v1/generate' \\
  -H 'X-API-Key: df_你的key' \\
  -H 'Content-Type: application/json' \\
  -d '{
    "template_code": "audit-report",
    "external_ref": "ORDER-123",
    "format": "pdf",
    "data": { "项目编号": "DEMO-2026-001", "测试人员": "张三" }
  }'`}</Code>
            <Paragraph type="secondary">
              关键字段：<Text code>template_code</Text>（或 template_id）、<Text code>data</Text>（单份）/<Text code>records</Text>（批量）、
              <Text code>format</Text>=docx|pdf|both、<Text code>external_ref</Text>（幂等键，重复提交不重跑、不重扣签章）、
              <Text code>callback_url</Text>、<Text code>sync</Text>。单份小数据默认同步返回，直接给 <Text code>signed_url</Text> 免 Key 直链下载（默认 24h 有效）。
            </Paragraph>
          </Collapse.Item>

          {/* ---- 签章授权 ---- */}
          <Collapse.Item header="六、电子签名 / 印章与授权（V3）" name="sign">
            <Paragraph>
              <Tag color="orange" size="small">合规边界</Tag>{' '}
              本产品签章是「图片型版式元素 + 全程审计」，不是 CA 数字证书 / PDF 密码学签名 / TSA 可信时间戳。
            </Paragraph>
            <Paragraph>
              <Text bold>准备：</Text>签章中心 → 签名库/印章库 → 新建。签名可上传透明底 PNG，或用姓名 + 5 款内置手写字体
              （马善政/刘建毛草/字芒星/龙藏/Dancing Script）生成，可选「带服务器时间」组合图；印章可上传或参数化生成圆形/椭圆红章。
            </Paragraph>
            <Paragraph>
              <Text bold>安放：</Text>在线编辑器「插入签名/印章」→ 抽屉只列出已审核且对你可用的素材 → 点选插入，占位变量名即资源 ID（<Text code>sig_xxx</Text> / <Text code>seal_xxx</Text>），走图片同管线，版式不漂移。
            </Paragraph>
            <Paragraph>
              <Text bold>两道独立关卡：</Text>
            </Paragraph>
            <Table
              size="small"
              pagination={false}
              data={[
                { lvl: '审核', rule: '系统设置里独立开关（默认关=免审直接用）；开启后新素材 pending，admin 通过才可用；未通过调用 → 422 signature_unavailable' },
                { lvl: '授权', rule: 'owner/admin 始终可用不扣次；其他人/Key 必须显式授权（次数 max_uses + 时间窗口 valid_from/valid_until 可组合）；未授权 → 403 grant_denied' },
              ]}
              columns={[
                { title: '关卡', dataIndex: 'lvl', width: 80, render: (v) => <Tag size="small" color="arcoblue">{v}</Tag> },
                { title: '规则', dataIndex: 'rule' },
              ]}
            />
            <Paragraph style={{ marginTop: 8 }}>
              <Text bold>扣次规则：</Text>预览不扣；owner/admin 不扣；一次 job 出 docx+pdf 只扣 1 次（按 job 不按格式）；
              同 external_ref 幂等重放不重扣。
            </Paragraph>
          </Collapse.Item>

          {/* ---- 审计日志 ---- */}
          <Collapse.Item header="七、审计日志（合规追溯）" name="audit">
            <Paragraph>普通用户只查本人；管理员在「管理中心 → 审计日志」查全量、做完整性校验、导出 CSV。</Paragraph>
            <Paragraph>
              每条记录：调用人（user/api_key）、动作、对象、模板编号、external_ref、用了哪个签名/印章（sig_xxx/seal_xxx）、时间、来源 IP、接口。
              覆盖：模板变更、生成/重试、签章增删改与使用、授权授予/续期/吊销与拒绝、审核、登录、Key 变更、设置改动。
            </Paragraph>
            <Paragraph>
              <Text bold>哈希链防篡改：</Text>每条 <Text code>self_hash = SHA256(prev_hash + 内容)</Text>，创世 prev_hash=64 个 0，环环相扣。
              点「完整性校验」从头重算全链，断链时精确定位 <Text code>broken_at_seq</Text>。应用层无修改/删除接口，库内改一条即断链。
              导出 CSV 前 5 行带校验头（区间首尾哈希、条数、导出时间）供外部复核。
            </Paragraph>
          </Collapse.Item>
        </Collapse>
      </Card>

      {/* ============ 既有：语法速查 ============ */}
      <Card title="模板语法速查（在 Word 里这样写）" style={{ marginBottom: 16 }}>
        <div style={{ overflowX: 'auto' }}>
          <Table
            rowKey="name"
            pagination={false}
            data={CHEATSHEET}
            tableLayoutFixed
            border={{ cell: true }}
            columns={[
              { title: '能力', dataIndex: 'name', width: 100, render: (v: string) => <Tag color="arcoblue">{v}</Tag> },
              {
                title: 'Word 写法',
                dataIndex: 'word',
                width: 380,
                render: (v: string) => <pre className="df-codeblock" style={{ margin: 0, padding: 8, whiteSpace: 'pre', overflowX: 'auto', maxWidth: 360 }}>{v}</pre>,
              },
              {
                title: 'JSON 侧数据',
                dataIndex: 'json',
                width: 460,
                render: (v: string) => <pre className="df-codeblock" style={{ margin: 0, padding: 8, whiteSpace: 'pre', overflowX: 'auto', maxWidth: 440 }}>{v}</pre>,
              },
              { title: '说明', dataIndex: 'note', width: 280, render: (v: string) => <span style={{ lineHeight: 1.6, wordBreak: 'break-word' }}>{v}</span> },
            ]}
          />
        </div>
      </Card>

      <Card title="常见错误与怎么改">
        <Table
          rowKey="code"
          pagination={false}
          data={ERROR_TABLE}
          columns={[
            { title: '错误', dataIndex: 'code', width: 200, render: (v: string) => <Tag color="red">{v}</Tag> },
            { title: '含义', dataIndex: 'meaning' },
            { title: '怎么改', dataIndex: 'fix' },
          ]}
        />
      </Card>
    </div>
  );
}
