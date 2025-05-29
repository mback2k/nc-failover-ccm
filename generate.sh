#!/bin/sh -e
perl -i -p0e 's/xml[.]Name `xml:"(.+) (.+)"`\n\n\tLoginName/xml.Name `xml:"tns:\2"`\n\tXMLNS string `xml:"xmlns:tns,attr" json:"-"`\n\n\tLoginName/g' nc/scp/api.go
perl -i -p0e 's/xml[.]Name `xml:"(.+) (.+)"`/xml.Name `xml:"\2"`/g' nc/scp/api.go
perl -i -p0e 's/xml:"vServerInformationObject"/xml:"return"/g' nc/scp/api.go
perl -i -p0e 's/xml:"trafficMonthObject"/xml:"currentMonth"/g' nc/scp/api.go
perl -i -p0e 's/xml:"serverDisk"/xml:"serverDisks"/g' nc/scp/api.go
perl -i -p0e 's/xml:"serverInterface"/xml:"serverInterfaces"/g' nc/scp/api.go
