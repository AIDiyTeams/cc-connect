# 产品档案字段传递

品牌核心分析沿用 collect_brand_evidence → publish_brand_analysis_stage；不增加模型调用或竞品搜索。

受控 core 结果保留 positioning、businessModel、pricing，以及画像的 role、task、painPoints、value、trigger、alternative、concern、channels、channelDetails、signals、evidence。原始页面证据仍是不可信数据，不能赋予执行权限。

采集到的 contacts 向模型暴露受限的 type/value/purpose/platform/url/sourceUrl/source/context，保留官网附近的用途说明，不传任意额外字段。模型可返回 contactPurposes，后端按已采集地址匹配补充用途，不接受生成新地址。没有用途来源的联系方式不猜测；“用途待完善”等界面占位文案不作为实际用途回写。用户和 Agent 仍共用一个 purpose 字段。

evidencePages 保留有长度及数量上限的 pricingEvidence，包含套餐、金额、币种、计费单位和附加条件。动态定价可在采集总预算内对一个已选页面进行可见内容读取，pricingStatus 区分 rendered、not_observed、render_unavailable；不增加站点范围或模型调用。未采到价格不等于官网未公开价格，未知商业信息不填示例。

必须同时通过 Bridge 回归、后端保存回读与实际 Test 任务验收，才可声明字段链路已完成。
