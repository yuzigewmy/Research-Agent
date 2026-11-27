import { isTemplateId, templates } from './projects'
import type { TemplateId } from './projects'
import type { ProjectTemplate, TemplateDefaults } from './projectTemplates'
import { courseDisciplineLabels, courseMethodLabels, courseReportTypeLabels } from './workflow'
import './workflow.css'

interface TemplatePickerProps {
  id: string
  items: ProjectTemplate[]
  value?: TemplateId
  disabled?: boolean
  onChange: (templateId: TemplateId) => void
}

const degreeLabels = { bachelor: '本科', master: '硕士', doctor: '博士' }
const degreeTypeLabels = { academic: '学术学位', professional: '专业学位' }

function defaultSettings(defaults: TemplateDefaults) {
  const fields: Array<[string, string]> = []
  if (defaults.checkpoints !== undefined) fields.push(['确认检查点', defaults.checkpoints ? '开启' : '关闭'])
  if (defaults.language) fields.push(['稿件语言', defaults.language === 'zh' ? '中文' : '英文'])
  if (defaults.targetPaperCount !== undefined) fields.push(['目标文献数', `${defaults.targetPaperCount} 篇`])
  if (defaults.proposal) {
    const { degree, degreeType, targetCharacters, targetSources } = defaults.proposal
    fields.push(
      ['学历层次', degreeLabels[degree]],
      ['学位类型', degreeTypeLabels[degreeType]],
      ['目标正文字数', `${targetCharacters} 字`],
      ['目标文献数', `${targetSources} 条`],
    )
  }
  if (defaults.course) {
    const { targetCharacters, discipline, method, reportType } = defaults.course
    fields.push(['稿件语言', '中文'], ['目标正文字数', `${targetCharacters} 字`], ['学科方向', courseDisciplineLabels[discipline]])
    if (method) fields.push(['研究方法', courseMethodLabels[method]])
    if (reportType) fields.push(['报告类型', courseReportTypeLabels[reportType]])
  }
  if (defaults.software) fields.push(['软件版本号', defaults.software.version])
  if (defaults.maxRounds !== undefined) fields.push(['最多审稿轮次', `${defaults.maxRounds} 轮`])
  if (defaults.targetScore !== undefined) fields.push(['目标分数', `${defaults.targetScore} 分`])
  if (defaults.maxStepRetries !== undefined) fields.push(['单步失败重试上限', `${defaults.maxStepRetries} 次`])
  if (defaults.contestId) {
    const contest = defaults.contests?.find((item) => item.id === defaults.contestId)
    fields.push(['默认竞赛规格', contest ? contest.name : defaults.contestId])
  }
  if (defaults.contests?.length) fields.push(['内置公开赛事', `覆盖 ${defaults.contests.length} 项公开赛事（2026 规则校验）`])
  return fields
}

function TemplateSpecification({ template, summaryId }: { template: ProjectTemplate; summaryId: string }) {
  const { revision, summary, deliverables, steps } = template
  const settings = defaultSettings(template.defaults)
  const delivery = deliverables.length > 0 && <p className="workflow-note">交付物 · <bdi>{deliverables.join('、')}</bdi></p>
  return <>
    <p id={summaryId} className="workflow-note" dir="auto">{summary}</p>
    {steps.length === 0 && delivery}
    {steps.length === 0 && settings.length === 0 ? (
      <p className="workflow-note">修订 <bdi>{revision}</bdi></p>
    ) : (
      <details className="workflow-artifact-history">
        <summary>模板规格 · 修订 <bdi>{revision}</bdi></summary>
        <div className="workflow-run-history-content">
          {steps.length > 0 && <>
            <ol className="workflow-steps" aria-label="模板步骤">
              {steps.map((step) => {
                const attributes = [
                  step.optional && '可跳过',
                  step.checkpoint === 'confirm' && '确认检查点',
                  step.checkpoint === 'choose-topic' && '选择题目',
                  step.acceptMarkdown && '可用已有 Markdown 验收',
                  step.requiresModel ? '生成需要模型' : '无需模型',
                ].filter(Boolean)
                return <li key={step.key}>
                  <div className="workflow-step-heading"><strong dir="auto">{step.title}</strong></div>
                  <p className="workflow-note">{attributes.join(' · ')}</p>
                </li>
              })}
            </ol>
            {delivery}
          </>}
          {settings.length > 0 && <>
            <strong>默认设置</strong>
            <dl className="project-properties">
              {settings.map(([label, value], index) => <div key={`${label}-${index}`}><dt>{label}</dt><dd>{value}</dd></div>)}
            </dl>
          </>}
        </div>
      </details>
    )}
  </>
}

export function ProjectTemplatePicker({ id, items, value, disabled, onChange }: TemplatePickerProps) {
  const selected = items.find((item) => item.id === value)
  const summaryId = `${id}-summary`
  return <div className="field">
    <label htmlFor={id}>项目模板</label>
    <select id={id} name="templateId" value={value ?? ''} disabled={disabled || items.length === 0}
      aria-describedby={selected ? summaryId : undefined}
      onChange={(event) => {
        const next = event.currentTarget.value
        if (isTemplateId(next) && items.some((item) => item.id === next)) onChange(next)
      }}>
      {value === undefined && <option value="" disabled>暂无可选模板</option>}
      {value !== undefined && !selected && <option value={value} disabled>{templates[value]}</option>}
      {items.map((item) => <option key={item.id} value={item.id}>{item.label}</option>)}
    </select>
    {selected && <TemplateSpecification template={selected} summaryId={summaryId} />}
  </div>
}
