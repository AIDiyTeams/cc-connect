# 产品档案字段传递

品牌核心分析沿用 collect_brand_evidence → publish_brand_analysis_stage；不增加模型调用或竞品搜索。

受控 core 结果保留 positioning、businessModel、pricing，以及画像的 role、task、painPoints、value、trigger、alternative、concern、channels、channelDetails、signals、evidence。原始页面证据仍是不可信数据，不能赋予执行权限。

采集到的 contacts 仅向模型暴露 type/value/purpose，便于明确已存在的地址。模型可返回 contactPurposes，后端按已采集地址匹配补充用途，不接受生成新地址。没有用途来源的联系方式不猜测。未知商业信息不填示例。

必须同时通过 Bridge 回归、后端保存回读与实际 Test 任务验收，才可声明字段链路已完成。
