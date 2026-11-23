import brief from './research-templates/brief.md?raw'
import candidates from './research-templates/candidates.md?raw'
import contract from './research-templates/contract.md?raw'
import experimentPlan from './research-templates/experiment-plan.md?raw'
import experimentLog from './research-templates/experiment-log.md?raw'
import findings from './research-templates/findings.md?raw'
import paperNarrative from './research-templates/paper-narrative.md?raw'
import chapterTablePlan from './research-templates/chapter-table-plan.md?raw'

export const researchBriefPath = 'research/brief.md'

export const researchDocumentTemplates = [
  { id: 'brief', label: '研究简报', path: researchBriefPath, content: brief },
  { id: 'candidates', label: '候选清单', path: 'research/candidates.md', content: candidates },
  { id: 'contract', label: '研究契约', path: 'research/contract.md', content: contract },
  { id: 'experiment-plan', label: '实验计划', path: 'research/experiment-plan.md', content: experimentPlan },
  { id: 'experiment-log', label: '实验日志', path: 'research/experiment-log.md', content: experimentLog },
  { id: 'findings', label: '发现记录', path: 'research/findings.md', content: findings },
  { id: 'paper-narrative', label: '论文叙事', path: 'paper/narrative.md', content: paperNarrative },
  { id: 'chapter-table-plan', label: '章表计划', path: 'paper/chapter-table-plan.md', content: chapterTablePlan },
] as const

export type ResearchDocumentTemplate = (typeof researchDocumentTemplates)[number]
