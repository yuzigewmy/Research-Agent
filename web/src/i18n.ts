import { createContext, useContext } from 'react'
import type { TemplateId } from './projects'

export type WorkbenchLanguage = 'zh-CN' | 'en'

const zhMessages = {
  // App & Masthead
  'app.title': '研究工作台',
  'app.newProject': '新建项目',
  'app.projectDetails': '项目详情',
  'app.loadingCatalog': '正在读取模板目录…',
  'app.catalogErrorPrefix': '读取模板目录失败。',
  'app.retryCatalog': '重试读取模板',

  // Preferences
  'prefs.theme': '主题',
  'prefs.theme.system': '跟随系统',
  'prefs.theme.light': '浅色',
  'prefs.theme.dark': '深色',
  'prefs.language': '界面语言',
  'prefs.showProjects': '展开项目列表',
  'prefs.hideProjects': '收起项目列表',

  // Notifications
  'notifications.title': '通知',
  'notifications.desktopUnsupported': '当前浏览器环境可使用站内通知。',
  'notifications.desktopDenied': '浏览器已禁止桌面通知。可在站点设置中修改权限，站内通知仍可使用。',
  'notifications.desktopEnabled': '当前标签页已开启桌面通知。',
  'notifications.desktopPrompt': '开启后，当前标签页可接收新的桌面通知。',
  'notifications.disableDesktop': '关闭桌面通知',
  'notifications.checkPermission': '检查通知权限',
  'notifications.enableDesktop': '开启桌面通知',
  'notifications.listLabel': '站内通知',
  'notifications.read': '已读',
  'notifications.unread': '未读',
  'notifications.openReview': '打开评审',
  'notifications.openWorkflow': '打开工作流',
  'notifications.openTask': '打开任务',
  'notifications.markingRead': '正在标记…',
  'notifications.markRead': '标为已读',
  'notifications.markFailed': '标记失败，请重试。',
  'notifications.empty': '暂无通知。',
  'notifications.loading': '正在读取通知…',
  'notifications.loadFailed': '读取通知失败，请重试。',
  'notifications.retryLoad': '重试读取通知',
  'notifications.loadOlderFailed': '读取较早通知失败，请重试。',
  'notifications.loadingMore': '正在读取…',
  'notifications.retryLoadMore': '重试加载更多',
  'notifications.loadMore': '加载更多',

  // Home collapsible sections
  'section.modelConnection': '模型连接',
  'section.modelCapabilityCatalog': '模型能力目录',
  'section.modelTest': '测试连接',
  'section.modelUnconfigured': '模型未配置。',
  'section.computeNode': '计算节点',
  'section.computeNodeCheck': '检查节点',
  'section.computeNodeConfigured': '计算节点已配置。',
  'section.workspaceBackup': '工作区备份',
  'section.workspaceBackupDownload': '下载工作区备份',
  'section.workspaceComponents': '组件检查',
  'section.workspaceComponentsCheck': '检查组件',
  'section.workspaceComponentsRecheck': '重新检查组件',
  'section.archiveImport': '导入项目归档（ZIP）',
  'section.archivePreview': '预览归档清单',
  'section.archiveVerifying': '正在校验归档…',

  // Create Project Form
  'create.softwareName': '软件全称',
  'create.patentName': '技术方案名称',
  'create.projectName': '项目名称',
  'create.contestLabel': '目标数学建模赛事',
  'create.startModeLabel': '建项起始方式',
  'create.submit': '创建项目',
  'create.submitting': '正在创建…',

  // Project List
  'projects.title': '项目列表',
  'projects.scopeGroup': '项目范围',
  'projects.scope.READY': '当前项目',
  'projects.scope.ARCHIVED': '归档项目',
  'projects.searchGroup': '项目检索',
  'projects.searchLabel': '搜索显示名或研究题目',
  'projects.searchSubmit': '搜索',
  'projects.searchClear': '清除',
  'projects.loading': '正在加载项目…',
  'projects.emptySearch': '没有匹配的项目',
  'projects.emptyArchived': '暂无归档项目',
  'projects.emptyReady': '暂无当前项目',
  'projects.emptySearchHint': '可以换个关键词或切换项目范围。',
  'projects.emptyReadyHint': '新建的项目会显示在这里。',
  'projects.loadMore': '加载更多',

  // Project Details & Sub-panels
  'details.loading': '正在加载项目详情…',
  'details.retry': '重试',
  'details.researchTitle': '研究题目',
  'details.template': '项目模板',
  'details.goal': '研究目标',
  'details.deliverables': '预期交付物',
  'details.status': '状态',
  'details.status.archived': '已归档',
  'details.status.ready': '已就绪',
  'details.createdAt': '创建时间',
  'details.revision': '修订',

  'panel.administration': '项目管理',
  'panel.memorySummary': '研究记忆与来源链',
  'panel.backgroundTasks': '后台任务',
  'panel.conversation': '项目对话',
  'panel.researchPlan': '研究计划',
  'panel.turnTasks': '此对话的任务',
  'panel.restrictedScripts': '受限本机脚本',
  'panel.textWorkspace': '文本工作区',
  'panel.diagramWorkspace': '图表工作区',
  'panel.diagramFlowMode': '流程图画布',
  'panel.diagramHtmlMode': 'HTML 可编辑图',
  'panel.materials': '项目资料',
  'panel.uploadFiles': '上传文件',
  'panel.uploading': '正在上传…',
  'panel.literature': '项目文献库',
  'panel.survey': '问卷分析',
  'panel.artifacts': '项目成果库',
  'panel.workflow.completed': '本轮已完成',
  'panel.workflow.notStarted': '尚未启动',

  // In-page Assistant
  'assistant.title': '页内研究助手',
  'assistant.muted': '已静音',
  'assistant.mute': '静音提示',
  'assistant.collapse': '收起助手',
  'assistant.open': '展开助手',
  'assistant.desktopDenied': '浏览器桌面通知已被拒绝，已自动降级为站内通知提醒。',
  'assistant.desktopUnsupported': '当前环境不支持桌面通知，请使用顶部站内通知查看任务更新。',
  'assistant.mutedNotice': '页内助手提醒已静音，仍可通过下方快捷入口跳转。',
  'assistant.quickNav': '快捷入口',
  'assistant.navConversation': '跳转对话区',
  'assistant.navWorkflow': '跳转工作流',
  'assistant.navWorkspace': '跳转文本工作区',
  'assistant.navDiagrams': '跳转图表工作区',
  'assistant.emptyProjectHint': '打开或新建项目后，可在此快速跳转对话区、工作流与文本工作区。',
  'assistant.navNotifications': '查看未读通知',
  'assistant.navComponents': '环境组件检查',
} as const

export type I18nKey = keyof typeof zhMessages

const enMessages: Record<I18nKey, string> = {
  // App & Masthead
  'app.title': 'Research Workbench',
  'app.newProject': 'New Project',
  'app.projectDetails': 'Project Details',
  'app.loadingCatalog': 'Loading template catalog…',
  'app.catalogErrorPrefix': 'Failed to load template catalog. ',
  'app.retryCatalog': 'Retry Loading Templates',

  // Preferences
  'prefs.theme': 'Theme',
  'prefs.theme.system': 'System',
  'prefs.theme.light': 'Light',
  'prefs.theme.dark': 'Dark',
  'prefs.language': 'Language',
  'prefs.showProjects': 'Show Projects',
  'prefs.hideProjects': 'Hide Projects',

  // Notifications
  'notifications.title': 'Notifications',
  'notifications.desktopUnsupported': 'In-app notifications are available in this browser context.',
  'notifications.desktopDenied': 'Desktop notifications are blocked by the browser. You can change site permissions; in-app notifications remain active.',
  'notifications.desktopEnabled': 'Desktop notifications are enabled for this tab.',
  'notifications.desktopPrompt': 'Enable to receive desktop notifications in this tab.',
  'notifications.disableDesktop': 'Disable Desktop Notifications',
  'notifications.checkPermission': 'Check Notification Permission',
  'notifications.enableDesktop': 'Enable Desktop Notifications',
  'notifications.listLabel': 'In-app notifications',
  'notifications.read': 'Read',
  'notifications.unread': 'Unread',
  'notifications.openReview': 'Open Review',
  'notifications.openWorkflow': 'Open Workflow',
  'notifications.openTask': 'Open Task',
  'notifications.markingRead': 'Marking…',
  'notifications.markRead': 'Mark as Read',
  'notifications.markFailed': 'Failed to mark as read. Please try again.',
  'notifications.empty': 'No notifications yet.',
  'notifications.loading': 'Loading notifications…',
  'notifications.loadFailed': 'Failed to load notifications. Please try again.',
  'notifications.retryLoad': 'Retry Loading Notifications',
  'notifications.loadOlderFailed': 'Failed to load earlier notifications. Please try again.',
  'notifications.loadingMore': 'Loading…',
  'notifications.retryLoadMore': 'Retry Loading More',
  'notifications.loadMore': 'Load More',

  // Home collapsible sections
  'section.modelConnection': 'Model Connection',
  'section.modelCapabilityCatalog': 'Model Capability Catalog',
  'section.modelTest': 'Test Connection',
  'section.modelUnconfigured': 'Model is not configured.',
  'section.computeNode': 'Compute Node',
  'section.computeNodeCheck': 'Check Node',
  'section.computeNodeConfigured': 'Compute node is configured.',
  'section.workspaceBackup': 'Workspace Backup',
  'section.workspaceBackupDownload': 'Download Workspace Backup',
  'section.workspaceComponents': 'Component Check',
  'section.workspaceComponentsCheck': 'Check Components',
  'section.workspaceComponentsRecheck': 'Recheck Components',
  'section.archiveImport': 'Import Project Archive (ZIP)',
  'section.archivePreview': 'Preview Archive Manifest',
  'section.archiveVerifying': 'Verifying archive…',

  // Create Project Form
  'create.softwareName': 'Software Full Name',
  'create.patentName': 'Technical Solution Name',
  'create.projectName': 'Project Name',
  'create.contestLabel': 'Target Math Modeling Contest',
  'create.startModeLabel': 'Project Start Mode',
  'create.submit': 'Create Project',
  'create.submitting': 'Creating…',

  // Project List
  'projects.title': 'Projects',
  'projects.scopeGroup': 'Project Scope',
  'projects.scope.READY': 'Active Projects',
  'projects.scope.ARCHIVED': 'Archived Projects',
  'projects.searchGroup': 'Project Search',
  'projects.searchLabel': 'Search display name or research title',
  'projects.searchSubmit': 'Search',
  'projects.searchClear': 'Clear',
  'projects.loading': 'Loading projects…',
  'projects.emptySearch': 'No matching projects',
  'projects.emptyArchived': 'No archived projects',
  'projects.emptyReady': 'No active projects',
  'projects.emptySearchHint': 'Try another keyword or switch project scope.',
  'projects.emptyReadyHint': 'Newly created projects will appear here.',
  'projects.loadMore': 'Load More',

  // Project Details & Sub-panels
  'details.loading': 'Loading project details…',
  'details.retry': 'Retry',
  'details.researchTitle': 'Research Title',
  'details.template': 'Template',
  'details.goal': 'Research Goal',
  'details.deliverables': 'Deliverables',
  'details.status': 'Status',
  'details.status.archived': 'Archived',
  'details.status.ready': 'Ready',
  'details.createdAt': 'Created',
  'details.revision': 'Revision',

  'panel.administration': 'Project Administration',
  'panel.memorySummary': 'Research Memory & Provenance',
  'panel.backgroundTasks': 'Background Tasks',
  'panel.conversation': 'Conversation',
  'panel.researchPlan': 'Research Plan',
  'panel.turnTasks': 'Tasks for This Turn',
  'panel.restrictedScripts': 'Restricted Local Scripts',
  'panel.textWorkspace': 'Text Workspace',
  'panel.diagramWorkspace': 'Diagram Workspace',
  'panel.diagramFlowMode': 'Flowchart Canvas',
  'panel.diagramHtmlMode': 'Editable HTML Diagram',
  'panel.materials': 'Project Materials',
  'panel.uploadFiles': 'Upload Files',
  'panel.uploading': 'Uploading…',
  'panel.literature': 'Literature Library',
  'panel.survey': 'Survey Analysis',
  'panel.artifacts': 'Project Artifacts',
  'panel.workflow.completed': 'Completed',
  'panel.workflow.notStarted': 'Not Started',

  // In-page Assistant
  'assistant.title': 'In-Page Assistant',
  'assistant.muted': 'Muted',
  'assistant.mute': 'Mute',
  'assistant.collapse': 'Collapse',
  'assistant.open': 'Open',
  'assistant.desktopDenied': 'Browser desktop notifications are blocked. In-app notification center remains active.',
  'assistant.desktopUnsupported': 'Desktop notifications are unavailable in this context; check the in-app notification center.',
  'assistant.mutedNotice': 'Assistant alerts are muted.',
  'assistant.quickNav': 'Quick Navigation',
  'assistant.navConversation': 'Conversation',
  'assistant.navWorkflow': 'Workflow',
  'assistant.navWorkspace': 'Text Workspace',
  'assistant.navDiagrams': 'Diagrams',
  'assistant.emptyProjectHint': 'Open or create a project to jump directly to conversation, workflow, or manuscript workspace.',
  'assistant.navNotifications': 'Notifications',
  'assistant.navComponents': 'Components',
}

const messages: Record<WorkbenchLanguage, Record<I18nKey, string>> = {
  'zh-CN': zhMessages,
  en: enMessages,
}

export type WorkflowTemplateId = Exclude<
  TemplateId,
  'freeform' | 'automatic_review' | 'interactive_survey' | 'engineering_project'
>

const workflowTitles: Record<WorkbenchLanguage, Record<WorkflowTemplateId, string>> = {
  'zh-CN': {
    experiment: '实验工作流',
    literature_review: '文献综述工作流',
    paper: '论文写作工作流',
    topic: '研究选题工作流',
    proposal: '开题报告工作流',
    course_paper: '课程论文工作流',
    course_report: '课程报告工作流',
    software_registration: '软件著作权材料工作流',
    patent_disclosure: '专利技术交底工作流',
    full_research: '科研全流程工作流',
    chinese_paper: '中文论文工作流',
    humanities_paper: '人文社科论文工作流',
    thesis: '学位论文工作流',
    existing_materials: '已有材料成稿工作流',
    market_research: '市场调查工作流',
    math_modeling: '数学建模竞赛工作流',
    big_data_contest: '大数据竞赛工作流',
  },
  en: {
    experiment: 'Experiment Workflow',
    literature_review: 'Literature Review Workflow',
    paper: 'Paper Writing Workflow',
    topic: 'Research Topic Workflow',
    proposal: 'Proposal Workflow',
    course_paper: 'Course Paper Workflow',
    course_report: 'Course Report Workflow',
    software_registration: 'Software Registration Workflow',
    patent_disclosure: 'Patent Disclosure Workflow',
    full_research: 'Full Research Workflow',
    chinese_paper: 'Chinese Paper Workflow',
    humanities_paper: 'Humanities Paper Workflow',
    thesis: 'Thesis Workflow',
    existing_materials: 'Existing Materials Workflow',
    market_research: 'Market Research Workflow',
    math_modeling: 'Math Modeling Workflow',
    big_data_contest: 'Big Data Contest Workflow',
  },
}

const LocaleContext = createContext<WorkbenchLanguage>('zh-CN')

export const LocaleProvider = LocaleContext.Provider

export function useLocale(): WorkbenchLanguage {
  return useContext(LocaleContext)
}

export function t(locale: WorkbenchLanguage, key: I18nKey): string {
  return messages[locale][key]
}

export function workflowTitle(locale: WorkbenchLanguage, templateId: WorkflowTemplateId): string {
  return workflowTitles[locale][templateId]
}

export function unreadCountLabel(locale: WorkbenchLanguage, count: number): string {
  if (count <= 0) return ''
  return locale === 'en' ? `${count} unread` : `${count} 条未读`
}
