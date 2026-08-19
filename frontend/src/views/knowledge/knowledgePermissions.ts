export interface KnowledgeDocumentActionVisibility {
  showDocumentActions: boolean
  showFolderManagement: boolean
}

/** 知识内容编辑与文件夹管理使用不同权限，避免共享 Editor 丢失上传入口。 */
export function knowledgeDocumentActionVisibility(
  canEdit: boolean,
  canManage: boolean,
  shared = false,
): KnowledgeDocumentActionVisibility {
  return {
    showDocumentActions: canEdit,
    showFolderManagement: canManage && !shared,
  }
}

export function knowledgeMutationAllowed(
  canEdit: boolean,
  isViaShare: boolean,
  isOwner: boolean,
  isAdmin: boolean,
  isContributor: boolean,
): boolean {
  if (!canEdit) return false
  if (isViaShare) return isContributor
  return isOwner || isAdmin || isContributor
}
